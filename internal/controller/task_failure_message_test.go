package controller

import (
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

func failedJob(reason, message string) *batchv1.Job {
	return &batchv1.Job{Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{
		Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: reason, Message: message,
	}}}}
}

func podWithAgentTermination(name string, created time.Time, reason string, exitCode int32) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: metav1.NewTime(created)},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{
			{Name: "sidecar", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Completed"}}},
			{Name: kelos.AgentContainerName, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: reason, ExitCode: exitCode}}},
		}},
	}
}

func TestJobFailureMessage(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name string
		job  *batchv1.Job
		pods []corev1.Pod
		want string
	}{
		{
			name: "no details",
			job:  failedJob("", ""),
			want: "Task failed",
		},
		{
			name: "job condition and latest pod's agent container",
			job:  failedJob("BackoffLimitExceeded", "Job has reached the specified backoff limit"),
			pods: []corev1.Pod{
				podWithAgentTermination("issue-5544-r8ngg", now, "Error", 1),
				podWithAgentTermination("issue-5544-fbxmt", now.Add(-time.Hour), "OOMKilled", 137),
			},
			want: "Task failed: BackoffLimitExceeded: Job has reached the specified backoff limit; container kelos-agent in pod issue-5544-r8ngg terminated (reason=Error, exitCode=1)",
		},
		{
			name: "pod-level deadline without container termination",
			job:  failedJob("DeadlineExceeded", "Job was active longer than specified deadline"),
			pods: []corev1.Pod{{
				ObjectMeta: metav1.ObjectMeta{Name: "p1"},
				Status:     corev1.PodStatus{Reason: "DeadlineExceeded", Message: "Pod was active on the node longer than the specified deadline"},
			}},
			want: "Task failed: DeadlineExceeded: Job was active longer than specified deadline; pod p1: DeadlineExceeded: Pod was active on the node longer than the specified deadline",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := jobFailureMessage(tt.job, tt.pods); got != tt.want {
				t.Fatalf("jobFailureMessage() =\n  %q\nwant\n  %q", got, tt.want)
			}
		})
	}
}

func TestJobFailureMessageCapsLength(t *testing.T) {
	got := jobFailureMessage(failedJob("X", strings.Repeat("m", 5000)), nil)
	if len(got) > maxFailureMessageLen {
		t.Fatalf("Message length = %d, want <= %d", len(got), maxFailureMessageLen)
	}
}
