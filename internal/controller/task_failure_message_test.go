package controller

import (
	"fmt"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

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

func podFailurePolicyJob(podName string, exitCode int32, failed int32) *batchv1.Job {
	job := failedJob(batchv1.JobReasonPodFailurePolicy,
		fmt.Sprintf("Container kelos-agent for pod default/%s failed with exit code %d matching FailJob rule at index 1", podName, exitCode))
	job.Spec.BackoffLimit = ptr.To(int32(1))
	job.Status.Failed = failed
	return job
}

// agentPod returns a Pod whose agent container has memoryLimit (if non-empty)
// and terminated with reason/exitCode, recorded in state.terminated or, when
// inLastState is set, only in lastState.terminated.
func agentPod(name, memoryLimit, reason string, exitCode int32, inLastState bool) corev1.Pod {
	agent := corev1.Container{Name: kelos.AgentContainerName}
	if memoryLimit != "" {
		agent.Resources.Limits = corev1.ResourceList{corev1.ResourceMemory: resource.MustParse(memoryLimit)}
	}
	term := &corev1.ContainerStateTerminated{Reason: reason, ExitCode: exitCode}
	status := corev1.ContainerStatus{Name: kelos.AgentContainerName}
	if inLastState {
		status.LastTerminationState.Terminated = term
	} else {
		status.State.Terminated = term
	}
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: metav1.NewTime(time.Now())},
		Spec: corev1.PodSpec{Containers: []corev1.Container{
			{Name: "sidecar", Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("64Mi")}}},
			agent,
		}},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{
			{Name: "sidecar", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 137}}},
			status,
		}},
	}
}

func TestJobFailureMessagePodFailurePolicy(t *testing.T) {
	tests := []struct {
		name string
		job  *batchv1.Job
		pods []corev1.Pod
		want string
	}{
		{
			name: "issue-5451 OOMKilled at the memory limit",
			job:  podFailurePolicyJob("issue-5451-7v7f9", 137, 1),
			pods: []corev1.Pod{agentPod("issue-5451-7v7f9", "10Gi", "OOMKilled", 137, false)},
			want: "Task failed: agent container exited 137 (OOMKilled) — not retried: same limit would OOM again (memory limit 10Gi); pod issue-5451-7v7f9, attempt 1 of 2; " +
				"PodFailurePolicy: Container kelos-agent for pod default/issue-5451-7v7f9 failed with exit code 137 matching FailJob rule at index 1",
		},
		{
			name: "OOMKilled recorded only in lastState, no memory limit",
			job:  podFailurePolicyJob("p1", 137, 1),
			pods: []corev1.Pod{agentPod("p1", "", "OOMKilled", 137, true)},
			want: "Task failed: agent container exited 137 (OOMKilled) — not retried: a retry would OOM again; pod p1, attempt 1 of 2; " +
				"PodFailurePolicy: Container kelos-agent for pod default/p1 failed with exit code 137 matching FailJob rule at index 1",
		},
		{
			name: "autocompact thrash stop",
			job:  podFailurePolicyJob("issue-5478-clhgn", 143, 1),
			pods: []corev1.Pod{agentPod("issue-5478-clhgn", "10Gi", "Error", 143, false)},
			want: "Task failed: agent container exited 143 (Error) — not retried: agent was stopped with SIGTERM (e.g. kelos-capture autocompact-thrash stop); pod issue-5478-clhgn, attempt 1 of 2; " +
				"PodFailurePolicy: Container kelos-agent for pod default/issue-5478-clhgn failed with exit code 143 matching FailJob rule at index 1",
		},
		{
			name: "SIGKILL without OOM",
			job:  podFailurePolicyJob("p2", 137, 1),
			pods: []corev1.Pod{agentPod("p2", "10Gi", "Error", 137, false)},
			want: "Task failed: agent container exited 137 (Error) — not retried: agent was killed with SIGKILL; pod p2, attempt 1 of 2; " +
				"PodFailurePolicy: Container kelos-agent for pod default/p2 failed with exit code 137 matching FailJob rule at index 1",
		},
		{
			name: "Task-level policy failing on another exit code",
			job:  podFailurePolicyJob("p3", 1, 2),
			pods: []corev1.Pod{agentPod("p3", "", "Error", 1, false)},
			want: "Task failed: agent container exited 1 (Error) — not retried: exit code matched a podFailurePolicy FailJob rule; pod p3, attempt 2 of 2; " +
				"PodFailurePolicy: Container kelos-agent for pod default/p3 failed with exit code 1 matching FailJob rule at index 1",
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

func TestJobFailureMessageBackoffLimitUnchangedForExitCode1(t *testing.T) {
	job := failedJob("BackoffLimitExceeded", "Job has reached the specified backoff limit")
	job.Spec.BackoffLimit = ptr.To(int32(1))
	job.Status.Failed = 2
	got := jobFailureMessage(job, []corev1.Pod{agentPod("p1", "10Gi", "Error", 1, false)})
	want := "Task failed: BackoffLimitExceeded: Job has reached the specified backoff limit; container kelos-agent in pod p1 terminated (reason=Error, exitCode=1)"
	if got != want {
		t.Fatalf("jobFailureMessage() =\n  %q\nwant\n  %q", got, want)
	}
}

func TestJobFailureMessageCapsLength(t *testing.T) {
	got := jobFailureMessage(failedJob("X", strings.Repeat("m", 5000)), nil)
	if len(got) > maxFailureMessageLen {
		t.Fatalf("Message length = %d, want <= %d", len(got), maxFailureMessageLen)
	}
}
