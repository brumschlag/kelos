package conversion

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	v1alpha2 "github.com/kelos-dev/kelos/api/v1alpha2"
)

// TestTaskSpawnerRoundTrip_PreservesIssueLabelPolicy checks the assumption the
// new fields were added on: that spoke conversion is a plain JSON round-trip, so
// declaring the fields identically in both API versions is enough to make them
// lossless. If that assumption is wrong, a v1alpha1 read followed by a write
// would silently drop the policy and re-arm the respawn loop — the exact failure
// class the policy exists to close, so it is worth measuring rather than
// assuming.
func TestTaskSpawnerRoundTrip_PreservesIssueLabelPolicy(t *testing.T) {
	maxAttempts := int32(5)

	hub := &v1alpha2.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{Name: "ts", Namespace: "default"},
		Spec: v1alpha2.TaskSpawnerSpec{
			When: v1alpha2.When{
				GitHubIssues: &v1alpha2.GitHubIssues{
					Repo:      "owner/repo",
					Labels:    []string{"kelos-auto"},
					Reporting: &v1alpha2.GitHubReporting{Enabled: true},
					OnSuccess: &v1alpha2.GitHubIssueCompletion{
						RemoveLabels: []string{"kelos-auto"},
					},
					OnFailure: &v1alpha2.GitHubIssueFailurePolicy{
						MaxAttempts:        &maxAttempts,
						AttemptLabelPrefix: "kelos-attempt-",
						BlockedLabel:       "kelos-blocked",
						RemoveLabels:       []string{"kelos-auto"},
					},
				},
			},
		},
	}

	spoke := &v1alpha1.TaskSpawner{}
	if err := taskSpawnerFromHub(context.Background(), hub, spoke); err != nil {
		t.Fatalf("taskSpawnerFromHub() error = %v", err)
	}

	// The spoke must carry the policy, not just tolerate it.
	sgi := spoke.Spec.When.GitHubIssues
	if sgi == nil || sgi.OnSuccess == nil || len(sgi.OnSuccess.RemoveLabels) != 1 {
		t.Fatalf("onSuccess lost converting to the spoke: %+v", sgi)
	}
	if sgi.OnFailure == nil || sgi.OnFailure.MaxAttempts == nil || *sgi.OnFailure.MaxAttempts != 5 {
		t.Fatalf("onFailure lost converting to the spoke: %+v", sgi.OnFailure)
	}

	back := &v1alpha2.TaskSpawner{}
	if err := taskSpawnerToHub(context.Background(), spoke, back); err != nil {
		t.Fatalf("taskSpawnerToHub() error = %v", err)
	}

	gi := back.Spec.When.GitHubIssues
	if gi == nil {
		t.Fatal("githubIssues nil after the round trip")
	}
	if gi.OnSuccess == nil {
		t.Fatal("onSuccess dropped by the round trip")
	}
	if len(gi.OnSuccess.RemoveLabels) != 1 || gi.OnSuccess.RemoveLabels[0] != "kelos-auto" {
		t.Errorf("onSuccess.removeLabels = %v, want [kelos-auto]", gi.OnSuccess.RemoveLabels)
	}
	if gi.OnFailure == nil {
		t.Fatal("onFailure dropped by the round trip")
	}
	if gi.OnFailure.MaxAttempts == nil || *gi.OnFailure.MaxAttempts != 5 {
		t.Errorf("onFailure.maxAttempts = %v, want 5", gi.OnFailure.MaxAttempts)
	}
	if gi.OnFailure.AttemptLabelPrefix != "kelos-attempt-" {
		t.Errorf("onFailure.attemptLabelPrefix = %q", gi.OnFailure.AttemptLabelPrefix)
	}
	if gi.OnFailure.BlockedLabel != "kelos-blocked" {
		t.Errorf("onFailure.blockedLabel = %q", gi.OnFailure.BlockedLabel)
	}
	if len(gi.OnFailure.RemoveLabels) != 1 || gi.OnFailure.RemoveLabels[0] != "kelos-auto" {
		t.Errorf("onFailure.removeLabels = %v, want [kelos-auto]", gi.OnFailure.RemoveLabels)
	}
}

// TestTaskSpawnerRoundTrip_PreservesExplicitZeroCeiling: 0 means "no ceiling"
// and is distinct from unset. A round trip that folded it to nil would silently
// re-enable unbounded retries — or, read the other way, silently impose a
// ceiling where an operator asked for none.
func TestTaskSpawnerRoundTrip_PreservesExplicitZeroCeiling(t *testing.T) {
	zero := int32(0)

	hub := &v1alpha2.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{Name: "ts", Namespace: "default"},
		Spec: v1alpha2.TaskSpawnerSpec{
			When: v1alpha2.When{
				GitHubIssues: &v1alpha2.GitHubIssues{
					Repo:      "owner/repo",
					Reporting: &v1alpha2.GitHubReporting{Enabled: true},
					OnFailure: &v1alpha2.GitHubIssueFailurePolicy{MaxAttempts: &zero},
				},
			},
		},
	}

	spoke := &v1alpha1.TaskSpawner{}
	if err := taskSpawnerFromHub(context.Background(), hub, spoke); err != nil {
		t.Fatalf("taskSpawnerFromHub() error = %v", err)
	}
	back := &v1alpha2.TaskSpawner{}
	if err := taskSpawnerToHub(context.Background(), spoke, back); err != nil {
		t.Fatalf("taskSpawnerToHub() error = %v", err)
	}

	fp := back.Spec.When.GitHubIssues.OnFailure
	if fp == nil || fp.MaxAttempts == nil {
		t.Fatalf("an explicit zero ceiling was folded to unset: %+v", fp)
	}
	if *fp.MaxAttempts != 0 {
		t.Errorf("maxAttempts = %d, want 0", *fp.MaxAttempts)
	}
}
