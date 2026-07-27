package sessionupdate

import (
	"reflect"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/types"
)

func TestRequestRoundTrip(t *testing.T) {
	request := NewRequest(types.UID("pod-uid"), "desired-revision")
	encoded, err := Encode(request)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, request) {
		t.Fatalf("decoded request = %#v, want %#v", decoded, request)
	}
	if got := NewRequest(types.UID("other-pod"), "desired-revision"); got.ID == request.ID {
		t.Fatalf("request IDs match across Pod UIDs: %q", got.ID)
	}
	if got := NewRequest(types.UID("pod-uid"), "other-revision"); got.ID == request.ID {
		t.Fatalf("request IDs match across StatefulSet revisions: %q", got.ID)
	}
	deadline := time.Date(2026, time.July, 27, 12, 0, 0, 0, time.UTC)
	idleRequest := NewIdleSuspendRequest(types.UID("pod-uid"), deadline)
	if idleRequest.Operation != OperationIdleSuspend || idleRequest.ID == request.ID {
		t.Fatalf("idle suspension request = %#v", idleRequest)
	}
	if got := NewIdleSuspendRequest(types.UID("pod-uid"), deadline); !reflect.DeepEqual(got, idleRequest) {
		t.Fatalf("idle suspension request is not stable: %#v != %#v", got, idleRequest)
	}
	if got := NewIdleSuspendRequest(types.UID("pod-uid"), deadline.Add(time.Second)); got.ID == idleRequest.ID {
		t.Fatalf("idle suspension request IDs match across deadlines: %q", got.ID)
	}
}

func TestDecodeRejectsIncompleteRequest(t *testing.T) {
	if _, err := Decode(`{"id":"request"}`); err == nil {
		t.Fatal("Decode() accepted a request without podUID")
	}
	if _, err := Decode(`{"id":"request","podUID":"pod","operation":"Unknown"}`); err == nil {
		t.Fatal("Decode() accepted an unsupported operation")
	}
}

func TestDecodeDefaultsLegacyRequestOperation(t *testing.T) {
	request, err := Decode(`{"id":"request","podUID":"pod"}`)
	if err != nil {
		t.Fatal(err)
	}
	if request.Operation != OperationRuntimeUpdate {
		t.Fatalf("legacy request operation = %q, want %q", request.Operation, OperationRuntimeUpdate)
	}
}
