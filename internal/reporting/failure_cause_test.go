package reporting

import (
	"context"
	"strings"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

const thrashResponse = "Autocompact is thrashing: the context refilled to the limit within 3 turns of the previous compact, 3 times in a row. A file being read or a tool output is likely too large for the context window. Try reading in smaller chunks, or use /clear to start fresh."

func TestFormatFailedCommentIncludesResponseAndCost(t *testing.T) {
	got := FormatFailedComment("issue-5527", "Task failed", map[string]string{
		"response": b64(thrashResponse),
		"cost-usd": "3.9937039999999997",
	})
	want := "🤖 **Kelos Task Status**\n\n" +
		"Task `issue-5527` has **failed**. ❌\n\n" +
		"**Cause** (final agent message):\n\n" +
		"```text\n" + thrashResponse + "\n```\n\n" +
		"**Cost:** $3.99"
	if got != want {
		t.Fatalf("Comment mismatch\n got: %q\nwant: %q", got, want)
	}
}

func TestFormatFailedCommentWithoutResponseSaysSo(t *testing.T) {
	got := FormatFailedComment("issue-5544", "Task failed: container kelos-agent terminated (reason=OOMKilled, exitCode=137)", nil)
	for _, want := range []string{
		"Task `issue-5544` has **failed**. ❌",
		"No agent response was captured for this run.",
		"**Controller status:** Task failed: container kelos-agent terminated (reason=OOMKilled, exitCode=137)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Comment %q does not contain %q", got, want)
		}
	}
	if strings.Contains(got, "**Cost:**") {
		t.Errorf("Comment %q reports a cost that is not available", got)
	}
}

func TestFormatFailedCommentWithoutResponseOrStatus(t *testing.T) {
	got := FormatFailedComment("t", "", map[string]string{"cost-usd": "1.5"})
	if !strings.Contains(got, "No agent response was captured for this run.") {
		t.Errorf("Comment %q does not state that no response exists", got)
	}
	if strings.Contains(got, "Controller status") {
		t.Errorf("Comment %q has an empty controller status line", got)
	}
	if !strings.Contains(got, "**Cost:** $1.50") {
		t.Errorf("Comment %q does not include the cost", got)
	}
}

func TestFormatFailedCommentTruncatesLongResponse(t *testing.T) {
	long := strings.Repeat("a", maxFailureCauseChars+500)
	got := FormatFailedComment("t", "", map[string]string{"response": b64(long)})
	if !strings.Contains(got, strings.Repeat("a", maxFailureCauseChars)+"\n```") {
		t.Errorf("Comment does not keep the first %d characters", maxFailureCauseChars)
	}
	if strings.Contains(got, strings.Repeat("a", maxFailureCauseChars+1)) {
		t.Errorf("Comment keeps more than %d characters", maxFailureCauseChars)
	}
	if !strings.Contains(got, "_Truncated: showing the first 1500 of 2000 characters._") {
		t.Errorf("Comment %q is not marked as truncated", got)
	}
}

func TestFormatFailedCommentRedactsSecrets(t *testing.T) {
	secrets := []string{
		"ghp_" + strings.Repeat("A1b2", 9),
		"gho_" + strings.Repeat("Z9y8", 9),
		"github_pat_11ABCDEFG0" + strings.Repeat("x_Y1", 15),
		"AKIAIOSFODNN7EXAMPLE",
		"sk-ant-api03-" + strings.Repeat("Qw-_", 10),
		"sk-" + strings.Repeat("proj", 8),
		"xoxb-123456789012-1234567890123-" + strings.Repeat("aBcD", 6),
	}
	response := "I tried these credentials:\n" + strings.Join(secrets, "\n") + "\nand none worked."
	got := FormatFailedComment("t", "", map[string]string{"response": b64(response)})
	for _, secret := range secrets {
		if strings.Contains(got, secret) {
			t.Errorf("Comment leaks secret %q:\n%s", secret, got)
		}
	}
	if n := strings.Count(got, "[REDACTED]"); n != len(secrets) {
		t.Errorf("Comment has %d redactions, want %d:\n%s", n, len(secrets), got)
	}
	for _, keep := range []string{"I tried these credentials:", "and none worked."} {
		if !strings.Contains(got, keep) {
			t.Errorf("Comment dropped non-secret text %q", keep)
		}
	}
}

func TestFormatFailedCommentKeepsNonSecretLookalikes(t *testing.T) {
	text := "See the task-management-framework-migration-notes and desk-reservation-service-rollout docs."
	got := FormatFailedComment("t", "", map[string]string{"response": b64(text)})
	if !strings.Contains(got, text) || strings.Contains(got, "[REDACTED]") {
		t.Errorf("Comment redacted non-secret text:\n%s", got)
	}
}

func TestFormatFailedCommentRedactsBeforeTruncating(t *testing.T) {
	// A token straddling the truncation point must not leave a prefix
	// that is still a usable token fragment.
	secret := "ghp_" + strings.Repeat("A1b2", 9)
	response := strings.Repeat("a", maxFailureCauseChars-10) + secret
	got := FormatFailedComment("t", "", map[string]string{"response": b64(response)})
	if strings.Contains(got, "ghp_A1b2") {
		t.Errorf("Comment leaks a token fragment:\n%s", got)
	}
}

func TestFormatFailedCommentFenceCannotBeClosedByResponse(t *testing.T) {
	response := "before\n```\n@someone #123 **bold**\n````\nafter"
	got := FormatFailedComment("t", "", map[string]string{"response": b64(response)})
	if !strings.Contains(got, "`````text\n"+response+"\n`````") {
		t.Errorf("Response is not wrapped in a fence longer than its own backtick runs:\n%s", got)
	}
}

func TestFormatFailedCommentUndecodableResponse(t *testing.T) {
	got := FormatFailedComment("t", "", map[string]string{"response": "not base64 !!"})
	if !strings.Contains(got, "not base64 !!") {
		t.Errorf("Comment %q does not fall back to the raw response", got)
	}
}

func TestFormatFailedCommentRedactsControllerStatus(t *testing.T) {
	secret := "ghp_" + strings.Repeat("A1b2", 9)
	got := FormatFailedComment("t", "Task failed: token "+secret, nil)
	if strings.Contains(got, secret) {
		t.Errorf("Comment leaks secret from status message:\n%s", got)
	}
}

func TestReportTaskStatus_FailedCommentIncludesCause(t *testing.T) {
	server, records := newTestServer(t)
	defer server.Close()

	task := newTaskWithAnnotations("issue-5527", "default", kelos.TaskPhaseFailed, map[string]string{
		AnnotationGitHubReporting:   "enabled",
		AnnotationSourceNumber:      "5527",
		AnnotationSourceKind:        "issue",
		AnnotationGitHubCommentID:   "5555",
		AnnotationGitHubReportPhase: "accepted",
	})
	task.Status.Message = "Task failed"
	task.Status.Results = map[string]string{"response": b64(thrashResponse), "cost-usd": "3.99"}

	cl := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(task).Build()
	tr := &TaskReporter{
		Client:   cl,
		Reporter: &GitHubReporter{Owner: "owner", Repo: "repo", Token: "token", BaseURL: server.URL},
	}
	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if len(*records) != 1 {
		t.Fatalf("Expected 1 API call, got %d", len(*records))
	}
	if !strings.Contains((*records)[0].body, "```text\n"+thrashResponse+"\n```") {
		t.Fatalf("Comment body does not include the cause:\n%s", (*records)[0].body)
	}

	var updated kelos.Task
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(task), &updated); err != nil {
		t.Fatal(err)
	}
	if got := updated.Annotations[AnnotationGitHubReportPhase]; got != "failed" {
		t.Fatalf("Report phase = %q, want %q", got, "failed")
	}
}

func TestReportTaskStatus_FailedCommentUpdatedWhenOutputArrivesLate(t *testing.T) {
	server, records := newTestServer(t)
	defer server.Close()

	task := newTaskWithAnnotations("issue-5544", "default", kelos.TaskPhaseFailed, map[string]string{
		AnnotationGitHubReporting:   "enabled",
		AnnotationSourceNumber:      "5544",
		AnnotationSourceKind:        "issue",
		AnnotationGitHubCommentID:   "5555",
		AnnotationGitHubReportPhase: "accepted",
	})
	task.Status.Message = "Task failed"

	cl := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(task).Build()
	tr := &TaskReporter{
		Client:   cl,
		Reporter: &GitHubReporter{Owner: "owner", Repo: "repo", Token: "token", BaseURL: server.URL},
	}

	// Failed before the controller captured outputs: report without a cause.
	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if len(*records) != 1 || !strings.Contains((*records)[0].body, "No agent response was captured") {
		t.Fatalf("First report = %+v, want a failed comment stating no response", *records)
	}

	// The controller's output retry then fills in Results.
	var current kelos.Task
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(task), &current); err != nil {
		t.Fatal(err)
	}
	current.Status.Results = map[string]string{"response": b64(thrashResponse)}
	if err := tr.ReportTaskStatus(context.Background(), &current); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if len(*records) != 2 {
		t.Fatalf("Expected the failed comment to be updated once output arrived, got %d API calls", len(*records))
	}
	if (*records)[1].method != "update" || !strings.Contains((*records)[1].body, thrashResponse) {
		t.Fatalf("Second report = %+v, want an update with the cause", (*records)[1])
	}

	// A further reconcile with the same state is a no-op.
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(task), &current); err != nil {
		t.Fatal(err)
	}
	current.Status.Results = map[string]string{"response": b64(thrashResponse)}
	if err := tr.ReportTaskStatus(context.Background(), &current); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if len(*records) != 2 {
		t.Fatalf("Duplicate report: %d API calls", len(*records))
	}
}

func TestReportTaskStatus_SucceededCommentUnchanged(t *testing.T) {
	server, records := newTestServer(t)
	defer server.Close()

	task := newTaskWithAnnotations("t", "default", kelos.TaskPhaseSucceeded, map[string]string{
		AnnotationGitHubReporting: "enabled",
		AnnotationSourceNumber:    "1",
		AnnotationSourceKind:      "issue",
	})
	task.Status.Results = map[string]string{"response": b64("All done"), "cost-usd": "1.00"}
	cl := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(task).Build()
	tr := &TaskReporter{
		Client:   cl,
		Reporter: &GitHubReporter{Owner: "owner", Repo: "repo", Token: "token", BaseURL: server.URL},
	}
	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if len(*records) != 1 {
		t.Fatalf("Expected 1 API call, got %d", len(*records))
	}
	if want := FormatSucceededComment("t"); (*records)[0].body != want {
		t.Fatalf("Succeeded body = %q, want %q", (*records)[0].body, want)
	}
}
