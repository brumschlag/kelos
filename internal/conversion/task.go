package conversion

import (
	"context"
	"encoding/json"

	v1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	v1alpha2 "github.com/kelos-dev/kelos/api/v1alpha2"
)

const (
	preservedTaskPreCommandsAnnotation  = "kelos.dev/v1alpha2-pre-commands"
	preservedTaskPostCommandsAnnotation = "kelos.dev/v1alpha2-post-commands"
)

func taskToHub(_ context.Context, src *v1alpha1.Task, dst *v1alpha2.Task) error {
	dst.ObjectMeta = src.ObjectMeta
	if err := convertViaJSON(&src.Spec, &dst.Spec); err != nil {
		return err
	}
	foldTaskAgentConfigRefForward(&src.Spec, &dst.Spec)
	if err := restorePreservedTaskCommands(dst); err != nil {
		return err
	}
	return convertViaJSON(&src.Status, &dst.Status)
}

func taskFromHub(_ context.Context, src *v1alpha2.Task, dst *v1alpha1.Task) error {
	dst.ObjectMeta = src.ObjectMeta
	if err := convertViaJSON(&src.Spec, &dst.Spec); err != nil {
		return err
	}
	if err := backfillTaskLegacyWorkerFields(&src.Spec, &dst.Spec); err != nil {
		return err
	}
	if err := setPreservedTaskCommands(dst, &src.Spec); err != nil {
		return err
	}
	return convertViaJSON(&src.Status, &dst.Status)
}

// PreCommands and PostCommands exist only in v1alpha2, so converting down to
// v1alpha1 would drop them and converting back could not recover them.
//
// That silent loss breaks reconciliation outright rather than merely losing
// detail: the Task controller adds its finalizer with a full-object Update, which
// resubmits the spec. With a field missing, the CRD's `self == oldSelf`
// immutability rule rejects the update, so the finalizer is never added and the
// Task is never reconciled — it sits with an empty status and no pod.
//
// Stashing on annotations is the same approach AgentConfig already uses for
// fields v1alpha1 does not model (see preservedMCPValueFromEnvAnnotation).
func setPreservedTaskCommands(dst *v1alpha1.Task, src *v1alpha2.TaskSpec) error {
	if err := setPreservedCommandAnnotation(dst, preservedTaskPreCommandsAnnotation, src.PreCommands); err != nil {
		return err
	}
	return setPreservedCommandAnnotation(dst, preservedTaskPostCommandsAnnotation, src.PostCommands)
}

func setPreservedCommandAnnotation(dst *v1alpha1.Task, key string, commands [][]string) error {
	if len(commands) == 0 {
		// Absent rather than empty: a Task that never used the field must not
		// acquire an annotation, or every object would carry dead metadata.
		deleteAnnotation(dst.Annotations, key)
		return nil
	}
	data, err := json.Marshal(commands)
	if err != nil {
		return err
	}
	if dst.Annotations == nil {
		dst.Annotations = map[string]string{}
	}
	dst.Annotations[key] = string(data)
	return nil
}

func restorePreservedTaskCommands(dst *v1alpha2.Task) error {
	pre, err := preservedCommandsFromAnnotation(dst.Annotations, preservedTaskPreCommandsAnnotation)
	if err != nil {
		return err
	}
	post, err := preservedCommandsFromAnnotation(dst.Annotations, preservedTaskPostCommandsAnnotation)
	if err != nil {
		return err
	}
	// Only fill what conversion could not carry; a genuine v1alpha1 value wins.
	if len(dst.Spec.PreCommands) == 0 && len(pre) > 0 {
		dst.Spec.PreCommands = pre
	}
	if len(dst.Spec.PostCommands) == 0 && len(post) > 0 {
		dst.Spec.PostCommands = post
	}
	// The annotations describe the v1alpha1 representation only. Leaving them on
	// the hub object would persist them to storage and accumulate across trips.
	deleteAnnotation(dst.Annotations, preservedTaskPreCommandsAnnotation)
	deleteAnnotation(dst.Annotations, preservedTaskPostCommandsAnnotation)
	return nil
}

func preservedCommandsFromAnnotation(annotations map[string]string, key string) ([][]string, error) {
	raw, ok := annotations[key]
	if !ok || raw == "" {
		return nil, nil
	}
	var commands [][]string
	if err := json.Unmarshal([]byte(raw), &commands); err != nil {
		return nil, err
	}
	return commands, nil
}

func foldTaskAgentConfigRefForward(src *v1alpha1.TaskSpec, dst *v1alpha2.TaskSpec) {
	if len(dst.AgentConfigRefs) == 0 && src.AgentConfigRef != nil {
		dst.AgentConfigRefs = []v1alpha2.AgentConfigReference{{Name: src.AgentConfigRef.Name}}
	}
}

func backfillTaskLegacyWorkerFields(src *v1alpha2.TaskSpec, dst *v1alpha1.TaskSpec) error {
	if src.WorkerPoolRef != nil || src.Worker == nil {
		return nil
	}
	worker := src.Worker

	if dst.Type == "" {
		dst.Type = worker.Type
	}
	if dst.Credentials.Type == "" && worker.Credentials != nil {
		if err := convertViaJSON(worker.Credentials, &dst.Credentials); err != nil {
			return err
		}
	}
	if dst.Model == "" {
		dst.Model = worker.Model
	}
	if dst.Effort == "" {
		dst.Effort = worker.Effort
	}
	if dst.Image == "" {
		dst.Image = worker.Image
	}
	if dst.WorkspaceRef == nil && worker.WorkspaceRef != nil {
		if err := convertViaJSON(worker.WorkspaceRef, &dst.WorkspaceRef); err != nil {
			return err
		}
	}
	if len(dst.AgentConfigRefs) == 0 && len(worker.AgentConfigRefs) > 0 {
		if err := convertViaJSON(&worker.AgentConfigRefs, &dst.AgentConfigRefs); err != nil {
			return err
		}
	}
	if dst.PodOverrides == nil && worker.PodOverrides != nil {
		if err := convertViaJSON(worker.PodOverrides, &dst.PodOverrides); err != nil {
			return err
		}
	}
	return nil
}
