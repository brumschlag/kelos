package main

import (
	"testing"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/reporting"
	"github.com/kelos-dev/kelos/internal/source"
)

func issueItem() source.WorkItem {
	return source.WorkItem{ID: "42", Number: 42, Kind: "Issue"}
}

func int32p(v int32) *int32 { return &v }

// TestSourceAnnotations_StampsRemoveLabelsOnSuccess covers the wiring half of
// the fix: the reporter has no access to the TaskSpawner spec, so unless the
// spawner stamps the policy onto the Task at creation time the removal can never
// fire — the same shape as AnnotationGitHubCheckName.
func TestSourceAnnotations_StampsRemoveLabelsOnSuccess(t *testing.T) {
	ts := &kelos.TaskSpawner{
		Spec: kelos.TaskSpawnerSpec{
			When: kelos.When{
				GitHubIssues: &kelos.GitHubIssues{
					Reporting: &kelos.GitHubReporting{Enabled: true},
					OnSuccess: &kelos.GitHubIssueCompletion{
						RemoveLabels: []string{"kelos-auto", "needs-agent"},
					},
				},
			},
		},
	}

	annotations := sourceAnnotations(ts, issueItem())

	got := annotations[reporting.AnnotationGitHubRemoveLabelsOnSuccess]
	if got != "kelos-auto,needs-agent" {
		t.Errorf("Expected %q, got %q", "kelos-auto,needs-agent", got)
	}
}

func TestSourceAnnotations_NoRemoveLabelsAnnotationWhenUnset(t *testing.T) {
	ts := &kelos.TaskSpawner{
		Spec: kelos.TaskSpawnerSpec{
			When: kelos.When{
				GitHubIssues: &kelos.GitHubIssues{
					Reporting: &kelos.GitHubReporting{Enabled: true},
				},
			},
		},
	}

	annotations := sourceAnnotations(ts, issueItem())

	if _, ok := annotations[reporting.AnnotationGitHubRemoveLabelsOnSuccess]; ok {
		t.Error("Expected no remove-labels annotation when onSuccess is unset")
	}
	if _, ok := annotations[reporting.AnnotationGitHubFailureMaxAttempts]; ok {
		t.Error("Expected no failure-ceiling annotation when onFailure is unset")
	}
}

// TestSourceAnnotations_NoLabelPolicyWithoutReporting: the reporter is the only
// thing that acts on these annotations, and it returns early unless comment
// reporting is enabled. Stamping them anyway would leave an annotation that
// reads as configured and does nothing.
func TestSourceAnnotations_NoLabelPolicyWithoutReporting(t *testing.T) {
	ts := &kelos.TaskSpawner{
		Spec: kelos.TaskSpawnerSpec{
			When: kelos.When{
				GitHubIssues: &kelos.GitHubIssues{
					OnSuccess: &kelos.GitHubIssueCompletion{RemoveLabels: []string{"kelos-auto"}},
					OnFailure: &kelos.GitHubIssueFailurePolicy{MaxAttempts: int32p(3)},
				},
			},
		},
	}

	annotations := sourceAnnotations(ts, issueItem())

	if _, ok := annotations[reporting.AnnotationGitHubRemoveLabelsOnSuccess]; ok {
		t.Error("Expected no remove-labels annotation when reporting is disabled")
	}
	if _, ok := annotations[reporting.AnnotationGitHubFailureMaxAttempts]; ok {
		t.Error("Expected no failure-ceiling annotation when reporting is disabled")
	}
}

func TestSourceAnnotations_StampsFailurePolicy(t *testing.T) {
	ts := &kelos.TaskSpawner{
		Spec: kelos.TaskSpawnerSpec{
			When: kelos.When{
				GitHubIssues: &kelos.GitHubIssues{
					Reporting: &kelos.GitHubReporting{Enabled: true},
					OnFailure: &kelos.GitHubIssueFailurePolicy{
						MaxAttempts:        int32p(5),
						AttemptLabelPrefix: "try-",
						BlockedLabel:       "stuck",
						RemoveLabels:       []string{"kelos-auto"},
					},
				},
			},
		},
	}

	annotations := sourceAnnotations(ts, issueItem())

	for key, want := range map[string]string{
		reporting.AnnotationGitHubFailureMaxAttempts:        "5",
		reporting.AnnotationGitHubFailureAttemptLabelPrefix: "try-",
		reporting.AnnotationGitHubFailureBlockedLabel:       "stuck",
		reporting.AnnotationGitHubFailureRemoveLabels:       "kelos-auto",
	} {
		if got := annotations[key]; got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

// TestSourceAnnotations_FailurePolicyDefaults pins the defaults to the bead
// reaper's values, so an operator who writes `onFailure: {}` gets the same
// ceiling the sibling path already uses rather than an accidental no-op.
func TestSourceAnnotations_FailurePolicyDefaults(t *testing.T) {
	ts := &kelos.TaskSpawner{
		Spec: kelos.TaskSpawnerSpec{
			When: kelos.When{
				GitHubIssues: &kelos.GitHubIssues{
					Reporting: &kelos.GitHubReporting{Enabled: true},
					OnFailure: &kelos.GitHubIssueFailurePolicy{},
				},
			},
		},
	}

	annotations := sourceAnnotations(ts, issueItem())

	if got := annotations[reporting.AnnotationGitHubFailureMaxAttempts]; got != "3" {
		t.Errorf("Expected the default ceiling of 3, got %q", got)
	}
	if got := annotations[reporting.AnnotationGitHubFailureAttemptLabelPrefix]; got != reporting.DefaultAttemptLabelPrefix {
		t.Errorf("Expected the default attempt prefix, got %q", got)
	}
	if got := annotations[reporting.AnnotationGitHubFailureBlockedLabel]; got != reporting.DefaultBlockedLabel {
		t.Errorf("Expected the default blocked label, got %q", got)
	}
}

// TestSourceAnnotations_FailurePolicyExplicitZeroIsPreserved: 0 means "no
// ceiling". It must not be defaulted back to 3, or the escape hatch is a lie.
func TestSourceAnnotations_FailurePolicyExplicitZeroIsPreserved(t *testing.T) {
	ts := &kelos.TaskSpawner{
		Spec: kelos.TaskSpawnerSpec{
			When: kelos.When{
				GitHubIssues: &kelos.GitHubIssues{
					Reporting: &kelos.GitHubReporting{Enabled: true},
					OnFailure: &kelos.GitHubIssueFailurePolicy{MaxAttempts: int32p(0)},
				},
			},
		},
	}

	annotations := sourceAnnotations(ts, issueItem())

	if got := annotations[reporting.AnnotationGitHubFailureMaxAttempts]; got != "0" {
		t.Errorf("Expected an explicit 0 to be preserved, got %q", got)
	}
}

// TestSourceAnnotations_PullRequestsGetNoIssueLabelPolicy: the policy lives on
// GitHubIssues only, so a PR-sourced Task must not inherit it.
func TestSourceAnnotations_PullRequestsGetNoIssueLabelPolicy(t *testing.T) {
	ts := &kelos.TaskSpawner{
		Spec: kelos.TaskSpawnerSpec{
			When: kelos.When{
				GitHubPullRequests: &kelos.GitHubPullRequests{
					Reporting: &kelos.GitHubReporting{Enabled: true},
				},
			},
		},
	}

	annotations := sourceAnnotations(ts, source.WorkItem{ID: "7", Number: 7, Kind: "PR"})

	if _, ok := annotations[reporting.AnnotationGitHubRemoveLabelsOnSuccess]; ok {
		t.Error("Expected no issue label policy on a pull-request source")
	}
}
