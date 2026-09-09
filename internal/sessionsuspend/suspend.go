package sessionsuspend

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

const (
	// ResumeRequestAnnotation asks the Session controller to resume an idle-suspended Session.
	ResumeRequestAnnotation = "kelos.dev/session-idle-resume-request"
	// ResumeRequestTimeAnnotation records when the controller observed the resume request.
	ResumeRequestTimeAnnotation = "kelos.dev/session-idle-resume-request-time"
	// ResumeAcknowledgementAnnotation records that a client connected for the resume request.
	ResumeAcknowledgementAnnotation = "kelos.dev/session-idle-resume-acknowledgement"
	// ResumeAcknowledgementTimeAnnotation records when the controller observed the acknowledgement.
	ResumeAcknowledgementTimeAnnotation = "kelos.dev/session-idle-resume-acknowledgement-time"
	// IdlePolicyReason identifies a Session suspended by its idle policy.
	IdlePolicyReason = kelos.SessionReasonIdlePolicyTriggered
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

// ResumeRequested reports whether a client has asked to resume an idle-suspended Session.
func ResumeRequested(session *kelos.Session) bool {
	return session.Annotations[ResumeRequestAnnotation] != ""
}

// ResumeRequestTime returns when the controller observed the current resume request.
func ResumeRequestTime(session *kelos.Session) (time.Time, bool) {
	requestedAt, err := time.Parse(time.RFC3339Nano, session.Annotations[ResumeRequestTimeAnnotation])
	return requestedAt, err == nil
}

// ResumeAcknowledged reports whether a client connected for the current resume request.
func ResumeAcknowledged(session *kelos.Session) bool {
	requestValue := session.Annotations[ResumeRequestAnnotation]
	return requestValue != "" && session.Annotations[ResumeAcknowledgementAnnotation] == requestValue
}

// ResumeAcknowledgementTime returns when the controller observed the acknowledgement.
func ResumeAcknowledgementTime(session *kelos.Session) (time.Time, bool) {
	acknowledgedAt, err := time.Parse(time.RFC3339Nano, session.Annotations[ResumeAcknowledgementTimeAnnotation])
	return acknowledgedAt, err == nil
}

// RequestResume asks the controller to resume an idle-suspended Session.
func RequestResume(ctx context.Context, cl client.Client, key client.ObjectKey) (*kelos.Session, bool, error) {
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
		if !IsIdlePolicySuspended(&current) || ResumeRequested(&current) {
			session = &current
			requested = false
			return nil
		}

		original := current.DeepCopy()
		if current.Annotations == nil {
			current.Annotations = map[string]string{}
		}
		current.Annotations[ResumeRequestAnnotation] = uuid.NewString()
		delete(current.Annotations, ResumeRequestTimeAnnotation)
		delete(current.Annotations, ResumeAcknowledgementAnnotation)
		delete(current.Annotations, ResumeAcknowledgementTimeAnnotation)
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

// AcknowledgeResume records that a client connected for the given resume request.
func AcknowledgeResume(ctx context.Context, cl client.Client, key client.ObjectKey, requestValue string) (bool, error) {
	if requestValue == "" {
		return false, fmt.Errorf("acknowledging Session %q resume: request value must not be empty", key.Name)
	}
	var (
		acknowledged bool
		operation    = "getting"
	)
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		operation = "getting"
		var current kelos.Session
		if err := cl.Get(ctx, key, &current); err != nil {
			return err
		}
		if current.Annotations[ResumeRequestAnnotation] != requestValue || ResumeAcknowledged(&current) {
			acknowledged = false
			return nil
		}

		original := current.DeepCopy()
		current.Annotations[ResumeAcknowledgementAnnotation] = requestValue
		delete(current.Annotations, ResumeAcknowledgementTimeAnnotation)
		operation = "acknowledging"
		if err := cl.Patch(ctx, &current, client.MergeFromWithOptions(original, client.MergeFromWithOptimisticLock{})); err != nil {
			return err
		}
		acknowledged = true
		return nil
	}); err != nil {
		if operation == "getting" {
			return false, fmt.Errorf("getting Session %q to acknowledge resume: %w", key.Name, err)
		}
		return false, fmt.Errorf("acknowledging Session %q resume: %w", key.Name, err)
	}
	return acknowledged, nil
}
