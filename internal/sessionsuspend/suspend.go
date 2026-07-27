package sessionsuspend

import (
	"context"
	"fmt"

	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

const (
	// ResumeRequestAnnotation asks the Session controller to resume an idle-suspended Session.
	ResumeRequestAnnotation = "kelos.dev/session-idle-resume-request"
	// IdlePolicyReason identifies a Session suspended by its idle policy.
	IdlePolicyReason = "IdlePolicyTriggered"
)

// IsIdlePolicySuspended reports whether the Session is suspended by its idle policy.
func IsIdlePolicySuspended(session *kelos.Session) bool {
	if session.Status.Phase != kelos.SessionPhaseSuspended ||
		(session.Spec.Suspend != nil && *session.Spec.Suspend) {
		return false
	}
	ready := apiMeta.FindStatusCondition(session.Status.Conditions, kelos.SessionConditionReady)
	return ready != nil && ready.Status == metav1.ConditionFalse && ready.Reason == IdlePolicyReason
}

// RequestResume asks the controller to resume an idle-suspended Session.
func RequestResume(ctx context.Context, cl client.Client, key client.ObjectKey, requestID string) (*kelos.Session, bool, error) {
	if requestID == "" {
		return nil, false, fmt.Errorf("requesting Session %q resume: request ID must not be empty", key.Name)
	}

	var (
		session   *kelos.Session
		requested bool
		operation = "getting"
	)
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		operation = "getting"
		var current kelos.Session
		if err := cl.Get(ctx, key, &current); err != nil {
			return err
		}
		if !IsIdlePolicySuspended(&current) || current.Annotations[ResumeRequestAnnotation] != "" {
			session = &current
			requested = false
			return nil
		}

		original := current.DeepCopy()
		if current.Annotations == nil {
			current.Annotations = map[string]string{}
		}
		current.Annotations[ResumeRequestAnnotation] = requestID

		operation = "requesting"
		if err := cl.Patch(ctx, &current, client.MergeFromWithOptions(original, client.MergeFromWithOptimisticLock{})); err != nil {
			return err
		}
		session = &current
		requested = true
		return nil
	}); err != nil {
		if operation == "getting" {
			return nil, false, fmt.Errorf("getting Session %q for resume: %w", key.Name, err)
		}
		return nil, false, fmt.Errorf("requesting Session %q resume: %w", key.Name, err)
	}
	return session, requested, nil
}
