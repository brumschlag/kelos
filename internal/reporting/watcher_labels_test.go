package reporting

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

// labelOp records one GitHub API call against the label or comment surface, so
// a test can assert not just the end state of the labels but exactly which calls
// were spent getting there — which is what proves idempotence.
type labelOp struct {
	kind   string // "list" | "add" | "remove" | "create-comment" | "update-comment"
	number int
	labels []string // add
	label  string   // remove
	body   string   // comments
}

// labelServer is a fake GitHub that models the label surface well enough to
// exercise the branches that actually run in production: removing a label that
// is already absent must 404, and adding one that is present must be a no-op.
type labelServer struct {
	*httptest.Server
	mu     sync.Mutex
	labels map[int]map[string]bool
	ops    []labelOp
	// failRemove, when non-zero, is the status returned for every DELETE.
	failRemove int
	// failList, when non-zero, is the status returned for every label GET.
	failList int
}

func newLabelServer(t *testing.T, initial map[int][]string) *labelServer {
	t.Helper()

	ls := &labelServer{labels: map[int]map[string]bool{}}
	for number, names := range initial {
		set := map[string]bool{}
		for _, n := range names {
			set[n] = true
		}
		ls.labels[number] = set
	}

	var nextCommentID int64 = 1000

	ls.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ls.mu.Lock()
		defer ls.mu.Unlock()

		path := r.URL.Path
		if path == "/user" {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(githubUser{Login: "reporter"})
			return
		}

		// /repos/{owner}/{repo}/issues/...
		parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
		// repos, owner, repo, issues, <n|comments>, ...
		if len(parts) < 5 || parts[0] != "repos" || parts[3] != "issues" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		// Comment update: /repos/o/r/issues/comments/{id}
		if parts[4] == "comments" {
			var body createCommentRequest
			json.NewDecoder(r.Body).Decode(&body)
			ls.ops = append(ls.ops, labelOp{kind: "update-comment", body: body.Body})
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(commentResponse{})
			return
		}

		number, err := strconv.Atoi(parts[4])
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		switch {
		case len(parts) >= 6 && parts[5] == "comments":
			switch r.Method {
			case http.MethodGet:
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode([]commentResponse{})
			case http.MethodPost:
				var body createCommentRequest
				json.NewDecoder(r.Body).Decode(&body)
				nextCommentID++
				ls.ops = append(ls.ops, labelOp{kind: "create-comment", number: number, body: body.Body})
				w.WriteHeader(http.StatusCreated)
				json.NewEncoder(w).Encode(commentResponse{ID: nextCommentID})
			default:
				w.WriteHeader(http.StatusMethodNotAllowed)
			}

		case len(parts) >= 6 && parts[5] == "labels":
			if ls.labels[number] == nil {
				ls.labels[number] = map[string]bool{}
			}
			switch r.Method {
			case http.MethodGet:
				ls.ops = append(ls.ops, labelOp{kind: "list", number: number})
				if ls.failList != 0 {
					w.WriteHeader(ls.failList)
					w.Write([]byte(`{"message":"boom"}`))
					return
				}
				out := make([]labelResponse, 0, len(ls.labels[number]))
				for name := range ls.labels[number] {
					out = append(out, labelResponse{Name: name})
				}
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(out)

			case http.MethodPost:
				var body addLabelsRequest
				json.NewDecoder(r.Body).Decode(&body)
				ls.ops = append(ls.ops, labelOp{kind: "add", number: number, labels: body.Labels})
				for _, l := range body.Labels {
					ls.labels[number][l] = true
				}
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`[]`))

			case http.MethodDelete:
				// /repos/o/r/issues/{n}/labels/{label}
				if len(parts) < 7 {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				label := parts[6]
				ls.ops = append(ls.ops, labelOp{kind: "remove", number: number, label: label})
				if ls.failRemove != 0 {
					w.WriteHeader(ls.failRemove)
					w.Write([]byte(`{"message":"boom"}`))
					return
				}
				if !ls.labels[number][label] {
					// GitHub's real answer for a label that is not on the issue.
					w.WriteHeader(http.StatusNotFound)
					w.Write([]byte(`{"message":"Label does not exist"}`))
					return
				}
				delete(ls.labels[number], label)
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`[]`))

			default:
				w.WriteHeader(http.StatusMethodNotAllowed)
			}

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	return ls
}

func (ls *labelServer) opsOfKind(kind string) []labelOp {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	var out []labelOp
	for _, op := range ls.ops {
		if op.kind == kind {
			out = append(out, op)
		}
	}
	return out
}

func (ls *labelServer) labelsOn(number int) []string {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	out := make([]string, 0, len(ls.labels[number]))
	for name := range ls.labels[number] {
		out = append(out, name)
	}
	return out
}

func (ls *labelServer) hasLabel(number int, label string) bool {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	return ls.labels[number][label]
}

func newLabelTaskReporter(t *testing.T, ls *labelServer, task *kelos.Task) *TaskReporter {
	t.Helper()
	return &TaskReporter{
		Client: fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(task).Build(),
		Reporter: &GitHubReporter{
			Owner:   "owner",
			Repo:    "repo",
			Token:   "token",
			BaseURL: ls.URL,
		},
	}
}

// ---------------------------------------------------------------------------
// Success path: the trigger label comes off when the work lands.
// ---------------------------------------------------------------------------

func TestReportTaskStatus_RemovesTriggerLabelOnSucceeded(t *testing.T) {
	ls := newLabelServer(t, map[int][]string{42: {"kelos-auto", "P1"}})
	defer ls.Close()

	task := newTaskWithAnnotations("issue-42", "default", kelos.TaskPhaseSucceeded, map[string]string{
		AnnotationGitHubReporting:             "enabled",
		AnnotationSourceNumber:                "42",
		AnnotationSourceKind:                  "issue",
		AnnotationGitHubCommentID:             "5555",
		AnnotationGitHubReportPhase:           "accepted",
		AnnotationGitHubRemoveLabelsOnSuccess: "kelos-auto",
	})

	tr := newLabelTaskReporter(t, ls, task)

	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	removes := ls.opsOfKind("remove")
	if len(removes) != 1 {
		t.Fatalf("Expected exactly 1 label removal, got %d (%v)", len(removes), removes)
	}
	if removes[0].label != "kelos-auto" || removes[0].number != 42 {
		t.Errorf("Unexpected removal: %+v", removes[0])
	}
	if ls.hasLabel(42, "kelos-auto") {
		t.Error("Expected kelos-auto to be gone from the issue")
	}
	if !ls.hasLabel(42, "P1") {
		t.Error("Expected unrelated labels to be left alone, P1 was removed")
	}
}

func TestReportTaskStatus_RemovesMultipleTriggerLabelsOnSucceeded(t *testing.T) {
	ls := newLabelServer(t, map[int][]string{42: {"kelos-auto", "kelos-attempt-1"}})
	defer ls.Close()

	task := newTaskWithAnnotations("issue-42", "default", kelos.TaskPhaseSucceeded, map[string]string{
		AnnotationGitHubReporting:             "enabled",
		AnnotationSourceNumber:                "42",
		AnnotationGitHubCommentID:             "5555",
		AnnotationGitHubReportPhase:           "accepted",
		AnnotationGitHubRemoveLabelsOnSuccess: "kelos-auto, kelos-attempt-1",
	})

	tr := newLabelTaskReporter(t, ls, task)
	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if got := len(ls.opsOfKind("remove")); got != 2 {
		t.Fatalf("Expected 2 label removals, got %d", got)
	}
	if remaining := ls.labelsOn(42); len(remaining) != 0 {
		t.Errorf("Expected no labels left, got %v", remaining)
	}
}

// TestReportTaskStatus_NoLabelCallsWithoutConfiguration is the negative control:
// an unconfigured spawner must spend no label API calls at all, so this feature
// cannot perturb existing deployments.
func TestReportTaskStatus_NoLabelCallsWithoutConfiguration(t *testing.T) {
	ls := newLabelServer(t, map[int][]string{42: {"kelos-auto"}})
	defer ls.Close()

	for _, phase := range []kelos.TaskPhase{kelos.TaskPhaseSucceeded, kelos.TaskPhaseFailed, kelos.TaskPhaseRunning} {
		task := newTaskWithAnnotations("issue-42", "default", phase, map[string]string{
			AnnotationGitHubReporting: "enabled",
			AnnotationSourceNumber:    "42",
		})
		tr := newLabelTaskReporter(t, ls, task)
		if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
			t.Fatalf("phase %s: unexpected error: %v", phase, err)
		}
	}

	for _, kind := range []string{"remove", "add", "list"} {
		if got := ls.opsOfKind(kind); len(got) != 0 {
			t.Errorf("Expected no %q calls without configuration, got %d", kind, len(got))
		}
	}
	if !ls.hasLabel(42, "kelos-auto") {
		t.Error("Expected kelos-auto to be untouched when nothing is configured")
	}
}

// TestReportTaskStatus_DoesNotRemoveTriggerLabelOnAccepted and its failed
// sibling pin Brian's ruling: the label comes off when the work LANDS, and a
// failure keeps it so the issue can be retried.
func TestReportTaskStatus_DoesNotRemoveTriggerLabelOnAccepted(t *testing.T) {
	ls := newLabelServer(t, map[int][]string{42: {"kelos-auto"}})
	defer ls.Close()

	task := newTaskWithAnnotations("issue-42", "default", kelos.TaskPhaseRunning, map[string]string{
		AnnotationGitHubReporting:             "enabled",
		AnnotationSourceNumber:                "42",
		AnnotationGitHubRemoveLabelsOnSuccess: "kelos-auto",
	})

	tr := newLabelTaskReporter(t, ls, task)
	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if got := ls.opsOfKind("remove"); len(got) != 0 {
		t.Errorf("Expected no removals on the accepted phase, got %v", got)
	}
	if !ls.hasLabel(42, "kelos-auto") {
		t.Error("Expected kelos-auto to survive the accepted phase")
	}
}

func TestReportTaskStatus_DoesNotRemoveTriggerLabelOnFailed(t *testing.T) {
	ls := newLabelServer(t, map[int][]string{42: {"kelos-auto"}})
	defer ls.Close()

	task := newTaskWithAnnotations("issue-42", "default", kelos.TaskPhaseFailed, map[string]string{
		AnnotationGitHubReporting:             "enabled",
		AnnotationSourceNumber:                "42",
		AnnotationGitHubCommentID:             "5555",
		AnnotationGitHubReportPhase:           "accepted",
		AnnotationGitHubRemoveLabelsOnSuccess: "kelos-auto",
	})

	tr := newLabelTaskReporter(t, ls, task)
	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if got := ls.opsOfKind("remove"); len(got) != 0 {
		t.Errorf("Expected no removals on failure, got %v", got)
	}
	if !ls.hasLabel(42, "kelos-auto") {
		t.Error("Expected kelos-auto to survive a failure so the issue can be retried")
	}
}

// TestReportTaskStatus_LabelRemovalIsIdempotentAcrossReports re-invokes the
// reporter at the same phase. The phase guard should short-circuit, spending no
// second removal — and even if it did not, RemoveLabel's 404 handling means the
// call still succeeds. Both properties are asserted.
func TestReportTaskStatus_LabelRemovalIsIdempotentAcrossReports(t *testing.T) {
	ls := newLabelServer(t, map[int][]string{42: {"kelos-auto"}})
	defer ls.Close()

	task := newTaskWithAnnotations("issue-42", "default", kelos.TaskPhaseSucceeded, map[string]string{
		AnnotationGitHubReporting:             "enabled",
		AnnotationSourceNumber:                "42",
		AnnotationGitHubCommentID:             "5555",
		AnnotationGitHubReportPhase:           "accepted",
		AnnotationGitHubRemoveLabelsOnSuccess: "kelos-auto",
	})

	tr := newLabelTaskReporter(t, ls, task)

	for i := 1; i <= 3; i++ {
		if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
			t.Fatalf("Report %d: unexpected error: %v", i, err)
		}
	}

	if got := len(ls.opsOfKind("remove")); got != 1 {
		t.Errorf("Expected exactly 1 removal across 3 reports, got %d", got)
	}
}

// TestReportTaskStatus_RepeatedRemovalOn404StillSucceeds drives the removal
// twice through a task whose phase annotation was never persisted, which is the
// controller-restart shape. The second attempt hits GitHub's 404 and must not
// surface as an error.
func TestReportTaskStatus_RepeatedRemovalOn404StillSucceeds(t *testing.T) {
	ls := newLabelServer(t, map[int][]string{42: {"kelos-auto"}})
	defer ls.Close()

	newTask := func() *kelos.Task {
		return newTaskWithAnnotations("issue-42", "default", kelos.TaskPhaseSucceeded, map[string]string{
			AnnotationGitHubReporting:             "enabled",
			AnnotationSourceNumber:                "42",
			AnnotationGitHubCommentID:             "5555",
			AnnotationGitHubReportPhase:           "accepted",
			AnnotationGitHubRemoveLabelsOnSuccess: "kelos-auto",
		})
	}

	for i := 1; i <= 2; i++ {
		task := newTask()
		tr := newLabelTaskReporter(t, ls, task)
		if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
			t.Fatalf("Fresh reporter %d: unexpected error: %v", i, err)
		}
	}

	if got := len(ls.opsOfKind("remove")); got != 2 {
		t.Fatalf("Expected 2 removal attempts, got %d", got)
	}
	if ls.hasLabel(42, "kelos-auto") {
		t.Error("Expected kelos-auto to be gone")
	}
}

// TestReportTaskStatus_LabelRemovalFailureDoesNotFailTheTask is the branch that
// matters operationally: work landed, the comment posted, and the cleanup 403'd.
// The reconcile must still succeed and the phase must still be persisted —
// otherwise a cleanup problem re-posts a status comment or wedges a Task that
// did its job.
func TestReportTaskStatus_LabelRemovalFailureDoesNotFailTheTask(t *testing.T) {
	ls := newLabelServer(t, map[int][]string{42: {"kelos-auto"}})
	ls.failRemove = http.StatusForbidden
	defer ls.Close()

	task := newTaskWithAnnotations("issue-42", "default", kelos.TaskPhaseSucceeded, map[string]string{
		AnnotationGitHubReporting:             "enabled",
		AnnotationSourceNumber:                "42",
		AnnotationGitHubCommentID:             "5555",
		AnnotationGitHubReportPhase:           "accepted",
		AnnotationGitHubRemoveLabelsOnSuccess: "kelos-auto",
	})

	tr := newLabelTaskReporter(t, ls, task)

	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("A failed label removal must not fail the task, got: %v", err)
	}

	if got := len(ls.opsOfKind("remove")); got != 1 {
		t.Errorf("Expected the removal to have been attempted once, got %d", got)
	}
	if task.Annotations[AnnotationGitHubReportPhase] != "succeeded" {
		t.Errorf("Expected the succeeded phase to still be persisted, got %q",
			task.Annotations[AnnotationGitHubReportPhase])
	}
}

// ---------------------------------------------------------------------------
// Failure path: the retry ceiling.
// ---------------------------------------------------------------------------

func failedTaskWithCeiling(number int, extra map[string]string) *kelos.Task {
	annotations := map[string]string{
		AnnotationGitHubReporting:                 "enabled",
		AnnotationSourceNumber:                    strconv.Itoa(number),
		AnnotationGitHubCommentID:                 "5555",
		AnnotationGitHubReportPhase:               "accepted",
		AnnotationGitHubFailureMaxAttempts:        "3",
		AnnotationGitHubFailureRemoveLabels:       "kelos-auto",
		AnnotationGitHubFailureBlockedLabel:       "kelos-blocked",
		AnnotationGitHubFailureAttemptLabelPrefix: DefaultAttemptLabelPrefix,
	}
	for k, v := range extra {
		annotations[k] = v
	}
	return newTaskWithAnnotations(fmt.Sprintf("issue-%d", number), "default", kelos.TaskPhaseFailed, annotations)
}

func TestReportTaskStatus_FailureStampsFirstAttempt(t *testing.T) {
	ls := newLabelServer(t, map[int][]string{42: {"kelos-auto"}})
	defer ls.Close()

	task := failedTaskWithCeiling(42, nil)
	tr := newLabelTaskReporter(t, ls, task)

	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	adds := ls.opsOfKind("add")
	if len(adds) != 1 || len(adds[0].labels) != 1 || adds[0].labels[0] != "kelos-attempt-1" {
		t.Fatalf("Expected kelos-attempt-1 to be added, got %+v", adds)
	}
	if !ls.hasLabel(42, "kelos-auto") {
		t.Error("Below the ceiling the trigger label must stay so the issue is retried")
	}
	if ls.hasLabel(42, "kelos-blocked") {
		t.Error("Must not be blocked on the first failure")
	}
}

func TestReportTaskStatus_FailureAdvancesTheAttemptCount(t *testing.T) {
	// A gap in the sequence is deliberate: the count is the HIGHEST attempt
	// label present, not the number of labels, mirroring beads-reaper.py.
	ls := newLabelServer(t, map[int][]string{42: {"kelos-auto", "kelos-attempt-1", "kelos-attempt-2"}})
	defer ls.Close()

	task := failedTaskWithCeiling(42, nil)
	tr := newLabelTaskReporter(t, ls, task)

	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	adds := ls.opsOfKind("add")
	if len(adds) != 1 || adds[0].labels[0] != "kelos-attempt-3" {
		t.Fatalf("Expected kelos-attempt-3, got %+v", adds)
	}
	if ls.hasLabel(42, "kelos-blocked") {
		t.Error("attempt 3 of 3 is still within the ceiling; must not be blocked yet")
	}
	if !ls.hasLabel(42, "kelos-auto") {
		t.Error("Trigger label must survive attempt 3 of 3")
	}
}

// TestReportTaskStatus_FailureAtCeilingBlocksAndRemovesTrigger is the half of
// the fix that stops a structurally-failing issue burning a run for ever.
func TestReportTaskStatus_FailureAtCeilingBlocksAndRemovesTrigger(t *testing.T) {
	ls := newLabelServer(t, map[int][]string{42: {"kelos-auto", "kelos-attempt-3"}})
	defer ls.Close()

	task := failedTaskWithCeiling(42, nil)
	tr := newLabelTaskReporter(t, ls, task)

	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if !ls.hasLabel(42, "kelos-blocked") {
		t.Error("Expected kelos-blocked at the ceiling")
	}
	// This, not the blocked label, is what actually stops rediscovery: the live
	// spawner's excludeLabels does not list kelos-blocked.
	if ls.hasLabel(42, "kelos-auto") {
		t.Error("Expected the trigger label to be removed at the ceiling")
	}
	if got := ls.opsOfKind("add"); len(got) != 1 || got[0].labels[0] != "kelos-blocked" {
		t.Errorf("Expected only kelos-blocked to be added at the ceiling, got %+v", got)
	}

	// The explanation comment must say why, or a human finds a silently-stalled
	// issue with no account of what stopped it.
	comments := ls.opsOfKind("create-comment")
	if len(comments) != 1 {
		t.Fatalf("Expected 1 ceiling comment, got %d", len(comments))
	}
	for _, want := range []string{"retry ceiling", "kelos-blocked", "kelos-auto"} {
		if !strings.Contains(comments[0].body, want) {
			t.Errorf("Ceiling comment does not mention %q: %s", want, comments[0].body)
		}
	}
}

// TestReportTaskStatus_CeilingIsIdempotent re-reports an already-blocked issue.
// The label writes are idempotent on GitHub's side, but the comment is not — so
// an already-blocked issue must not collect a second explanation.
func TestReportTaskStatus_CeilingIsIdempotent(t *testing.T) {
	ls := newLabelServer(t, map[int][]string{42: {"kelos-attempt-3", "kelos-blocked"}})
	defer ls.Close()

	task := failedTaskWithCeiling(42, nil)
	tr := newLabelTaskReporter(t, ls, task)

	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if got := ls.opsOfKind("create-comment"); len(got) != 0 {
		t.Errorf("Expected no duplicate ceiling comment on an already-blocked issue, got %d: %+v", len(got), got)
	}
	if !ls.hasLabel(42, "kelos-blocked") {
		t.Error("Expected kelos-blocked to remain")
	}
}

// TestReportTaskStatus_NoCeilingWhenUnconfigured pins the compatibility
// promise: without the annotation, failure behaviour is exactly what it was.
func TestReportTaskStatus_NoCeilingWhenUnconfigured(t *testing.T) {
	ls := newLabelServer(t, map[int][]string{42: {"kelos-auto"}})
	defer ls.Close()

	task := newTaskWithAnnotations("issue-42", "default", kelos.TaskPhaseFailed, map[string]string{
		AnnotationGitHubReporting:   "enabled",
		AnnotationSourceNumber:      "42",
		AnnotationGitHubCommentID:   "5555",
		AnnotationGitHubReportPhase: "accepted",
	})

	tr := newLabelTaskReporter(t, ls, task)
	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if got := ls.opsOfKind("list"); len(got) != 0 {
		t.Errorf("Expected no label reads without a configured ceiling, got %d", len(got))
	}
	if got := ls.opsOfKind("add"); len(got) != 0 {
		t.Errorf("Expected no attempt label without a configured ceiling, got %d", len(got))
	}
}

// TestReportTaskStatus_CeilingDisabledByZero keeps an explicit escape hatch
// working: maxAttempts 0 restores unbounded retries.
func TestReportTaskStatus_CeilingDisabledByZero(t *testing.T) {
	ls := newLabelServer(t, map[int][]string{42: {"kelos-auto", "kelos-attempt-9"}})
	defer ls.Close()

	task := failedTaskWithCeiling(42, map[string]string{AnnotationGitHubFailureMaxAttempts: "0"})
	tr := newLabelTaskReporter(t, ls, task)

	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if got := ls.opsOfKind("add"); len(got) != 0 {
		t.Errorf("Expected no label writes when the ceiling is disabled, got %+v", got)
	}
	if ls.hasLabel(42, "kelos-blocked") {
		t.Error("Expected no blocking when the ceiling is disabled")
	}
}

// TestReportTaskStatus_CeilingListFailureDoesNothing: without the current
// labels the attempt count is unknown. Guessing would either re-stamp attempt 1
// for ever or jump straight to blocked, so the policy must decline to act — and
// must still not fail the Task.
func TestReportTaskStatus_CeilingListFailureDoesNothing(t *testing.T) {
	ls := newLabelServer(t, map[int][]string{42: {"kelos-auto"}})
	ls.failList = http.StatusForbidden
	defer ls.Close()

	task := failedTaskWithCeiling(42, nil)
	tr := newLabelTaskReporter(t, ls, task)

	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("A failed label read must not fail the task, got: %v", err)
	}

	if got := ls.opsOfKind("add"); len(got) != 0 {
		t.Errorf("Expected no label writes when the attempt count is unknown, got %+v", got)
	}
	if got := ls.opsOfKind("remove"); len(got) != 0 {
		t.Errorf("Expected no removals when the attempt count is unknown, got %+v", got)
	}
	if !ls.hasLabel(42, "kelos-auto") {
		t.Error("Expected the trigger label to be untouched")
	}
}

func TestHighestAttempt(t *testing.T) {
	cases := []struct {
		name   string
		labels []string
		prefix string
		want   int
	}{
		{"none", []string{"kelos-auto", "P1"}, DefaultAttemptLabelPrefix, 0},
		{"single", []string{"kelos-attempt-1"}, DefaultAttemptLabelPrefix, 1},
		{"highest wins regardless of order", []string{"kelos-attempt-3", "kelos-attempt-1"}, DefaultAttemptLabelPrefix, 3},
		{"double digits are not string-compared", []string{"kelos-attempt-9", "kelos-attempt-10"}, DefaultAttemptLabelPrefix, 10},
		{"suffix must be entirely numeric", []string{"kelos-attempt-1-retry", "kelos-attempt-2x"}, DefaultAttemptLabelPrefix, 0},
		{"prefix must match exactly", []string{"other-attempt-5"}, DefaultAttemptLabelPrefix, 0},
		{"custom prefix", []string{"try-7"}, "try-", 7},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := highestAttempt(tc.labels, tc.prefix); got != tc.want {
				t.Errorf("highestAttempt(%v, %q) = %d, want %d", tc.labels, tc.prefix, got, tc.want)
			}
		})
	}
}

func TestSplitAnnotationList(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"a", []string{"a"}},
		{"a,b", []string{"a", "b"}},
		{" a , b ", []string{"a", "b"}},
		{"a,,b", []string{"a", "b"}},
		{",", nil},
	}
	for _, tc := range cases {
		got := splitAnnotationList(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("splitAnnotationList(%q) = %v, want %v", tc.in, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("splitAnnotationList(%q) = %v, want %v", tc.in, got, tc.want)
				break
			}
		}
	}
}
