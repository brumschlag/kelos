package conversion

import (
	"context"
	"reflect"
	"testing"

	v1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	v1alpha2 "github.com/kelos-dev/kelos/api/v1alpha2"
)

// PreCommands/PostCommands exist only in v1alpha2, so a plain JSON conversion
// drops them on the way down to v1alpha1 and cannot recover them coming back.
//
// That is not merely cosmetic: the Task controller adds its finalizer with a
// full-object Update, which resubmits the spec. If conversion has stripped a
// field, the CRD's `self == oldSelf` immutability rule rejects the update, the
// finalizer is never added, and the Task is never reconciled — it sits with an
// empty status and no pod forever.
func TestTaskCommandsSurviveRoundTrip(t *testing.T) {
	src := &v1alpha2.Task{}
	src.Spec.Model = "claude-haiku"
	src.Spec.Prompt = "echo hi"
	src.Spec.PreCommands = [][]string{
		{"sh", "-c", "set -e; mkdir -p /tmp/x\ncat > /tmp/x/y.sh <<'EOF'\n#!/bin/sh\nexit 2\nEOF"},
		{"true"},
	}
	src.Spec.PostCommands = [][]string{{"sh", "-c", "echo done"}}

	var legacy v1alpha1.Task
	if err := taskFromHub(context.Background(), src, &legacy); err != nil {
		t.Fatalf("taskFromHub: %v", err)
	}

	var back v1alpha2.Task
	if err := taskToHub(context.Background(), &legacy, &back); err != nil {
		t.Fatalf("taskToHub: %v", err)
	}

	if !reflect.DeepEqual(back.Spec.PreCommands, src.Spec.PreCommands) {
		t.Errorf("preCommands not preserved:\n got %#v\nwant %#v", back.Spec.PreCommands, src.Spec.PreCommands)
	}
	if !reflect.DeepEqual(back.Spec.PostCommands, src.Spec.PostCommands) {
		t.Errorf("postCommands not preserved:\n got %#v\nwant %#v", back.Spec.PostCommands, src.Spec.PostCommands)
	}
}

// The preservation annotations are an implementation detail of the v1alpha1
// representation and must not leak back into the hub object, or every round trip
// would accumulate them on the stored resource.
func TestTaskCommandAnnotationsDoNotLeakToHub(t *testing.T) {
	src := &v1alpha2.Task{}
	src.Spec.PreCommands = [][]string{{"true"}}

	var legacy v1alpha1.Task
	if err := taskFromHub(context.Background(), src, &legacy); err != nil {
		t.Fatalf("taskFromHub: %v", err)
	}
	if _, ok := legacy.Annotations[preservedTaskPreCommandsAnnotation]; !ok {
		t.Fatalf("expected the legacy object to carry the preservation annotation")
	}

	var back v1alpha2.Task
	if err := taskToHub(context.Background(), &legacy, &back); err != nil {
		t.Fatalf("taskToHub: %v", err)
	}
	if _, ok := back.Annotations[preservedTaskPreCommandsAnnotation]; ok {
		t.Errorf("preservation annotation leaked onto the hub object")
	}
}

// A Task that uses neither field must not gain empty annotations.
func TestTaskWithoutCommandsGetsNoAnnotations(t *testing.T) {
	src := &v1alpha2.Task{}
	src.Spec.Prompt = "echo hi"

	var legacy v1alpha1.Task
	if err := taskFromHub(context.Background(), src, &legacy); err != nil {
		t.Fatalf("taskFromHub: %v", err)
	}
	if _, ok := legacy.Annotations[preservedTaskPreCommandsAnnotation]; ok {
		t.Errorf("unexpected preCommands annotation on a Task without preCommands")
	}
	if _, ok := legacy.Annotations[preservedTaskPostCommandsAnnotation]; ok {
		t.Errorf("unexpected postCommands annotation on a Task without postCommands")
	}
}
