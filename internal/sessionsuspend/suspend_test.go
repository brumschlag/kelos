package sessionsuspend

import (
	"context"
	"errors"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

type conflictOnceClient struct {
	client.Client
	patches int
}

func (c *conflictOnceClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	c.patches++
	if c.patches == 1 {
		return apierrors.NewConflict(
			schema.GroupResource{Group: "kelos.dev", Resource: "sessions"},
			obj.GetName(),
			errors.New("conflict"),
		)
	}
	return c.Client.Patch(ctx, obj, patch, opts...)
}

func TestIsIdlePolicySuspended(t *testing.T) {
	session := idleSuspendedSession()
	if !IsIdlePolicySuspended(session) {
		t.Fatal("IsIdlePolicySuspended() = false, want true")
	}

	session.Spec.Suspend = new(bool)
	*session.Spec.Suspend = true
	if IsIdlePolicySuspended(session) {
		t.Fatal("IsIdlePolicySuspended() = true for manually suspended Session")
	}
}

func TestRequestResumeMarksIdleSuspendedSessionOnce(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kelos.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	session := idleSuspendedSession()
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(session).Build()
	key := client.ObjectKeyFromObject(session)

	updated, requested, err := RequestResume(context.Background(), cl, key, "request-1")
	if err != nil {
		t.Fatal(err)
	}
	if !requested || updated.Annotations[ResumeRequestAnnotation] != "request-1" {
		t.Fatalf("RequestResume() = requested %t annotations %#v", requested, updated.Annotations)
	}

	updated, requested, err = RequestResume(context.Background(), cl, key, "request-2")
	if err != nil {
		t.Fatal(err)
	}
	if requested || updated.Annotations[ResumeRequestAnnotation] != "request-1" {
		t.Fatalf("second RequestResume() = requested %t annotations %#v", requested, updated.Annotations)
	}
}

func TestRequestResumeIgnoresManualSuspension(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kelos.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	session := idleSuspendedSession()
	session.Spec.Suspend = new(bool)
	*session.Spec.Suspend = true
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(session).Build()

	updated, requested, err := RequestResume(context.Background(), cl, client.ObjectKeyFromObject(session), "request-1")
	if err != nil {
		t.Fatal(err)
	}
	if requested || updated.Annotations[ResumeRequestAnnotation] != "" {
		t.Fatalf("RequestResume() = requested %t annotations %#v", requested, updated.Annotations)
	}
}

func TestRequestResumeRetriesPatchConflict(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kelos.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	session := idleSuspendedSession()
	cl := &conflictOnceClient{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(session).Build(),
	}

	_, requested, err := RequestResume(context.Background(), cl, client.ObjectKeyFromObject(session), "request-1")
	if err != nil {
		t.Fatal(err)
	}
	if !requested {
		t.Fatal("RequestResume() requested = false, want true")
	}
	if cl.patches != 2 {
		t.Fatalf("Patch() called %d times, want 2", cl.patches)
	}
}

func idleSuspendedSession() *kelos.Session {
	return &kelos.Session{
		ObjectMeta: metav1.ObjectMeta{Name: "chat", Namespace: "default"},
		Status: kelos.SessionStatus{
			Phase: kelos.SessionPhaseSuspended,
			Conditions: []metav1.Condition{{
				Type:   kelos.SessionConditionReady,
				Status: metav1.ConditionFalse,
				Reason: IdlePolicyReason,
			}},
		},
	}
}
