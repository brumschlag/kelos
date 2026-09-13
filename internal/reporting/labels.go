package reporting

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

// These counters exist because the defect they close survived for weeks purely
// through being unobservable: nothing removed the trigger label and nothing
// reported that nothing removed it. A cleanup step you cannot see firing is one
// you cannot trust, so every attempt is counted by outcome — a rising "error"
// rate, or a flat "success" count while Tasks keep succeeding, is the signal.
var (
	labelRemovalsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kelos_reporting_github_label_removals_total",
			Help: "GitHub trigger-label removal attempts by outcome (success, error)",
		},
		[]string{"outcome"},
	)

	failureAttemptsRecordedTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "kelos_reporting_github_failure_attempts_recorded_total",
			Help: "Failed-Task attempts stamped onto the originating GitHub issue",
		},
	)

	failureCeilingReachedTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "kelos_reporting_github_failure_ceiling_reached_total",
			Help: "Issues marked blocked because the retry ceiling was exhausted",
		},
	)
)

func init() {
	metrics.Registry.MustRegister(
		labelRemovalsTotal,
		failureAttemptsRecordedTotal,
		failureCeilingReachedTotal,
	)
}

// splitAnnotationList parses a comma-separated annotation value, dropping empty
// entries and trimming whitespace.
func splitAnnotationList(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// removeLabelsOnSuccess removes the configured trigger labels from the
// originating issue once its Task has succeeded.
//
// Best-effort by design: a label that cannot be removed must not fail a Task
// whose work landed, and must not cause the status comment to be re-posted. The
// cost of that choice is that a failed removal is NOT retried within this Task's
// lifetime — the next spawn of the same issue (after ttlSecondsAfterFinished)
// gets the next attempt. So a transient failure costs one extra cycle, and a
// persistent one (e.g. a token without Issues:write) shows up as a rising
// labelRemovalsTotal{outcome="error"} rather than as silence.
func (tr *TaskReporter) removeLabelsOnSuccess(ctx context.Context, task *kelos.Task, number int) {
	labels := splitAnnotationList(task.Annotations[AnnotationGitHubRemoveLabelsOnSuccess])
	if len(labels) == 0 {
		return
	}

	log := ctrl.Log.WithName("reporter")
	succeeded := 0

	for _, label := range labels {
		if err := tr.Reporter.RemoveLabel(ctx, number, label); err != nil {
			labelRemovalsTotal.WithLabelValues("error").Inc()
			// Loud on purpose. A silent removal failure re-arms the respawn loop
			// invisibly, which is the exact failure mode this code closes.
			log.Error(err, "Failed to remove GitHub trigger label after success; the issue will be rediscovered once its Task is TTL-deleted",
				"task", task.Name, "number", number, "label", label)
			continue
		}
		labelRemovalsTotal.WithLabelValues("success").Inc()
		succeeded++
	}

	log.Info("Removed GitHub trigger labels after success",
		"task", task.Name, "number", number,
		"attempted", len(labels), "succeeded", succeeded, "labels", strings.Join(labels, ","))
}

// applyFailurePolicy bounds retries for an issue whose Task failed. It mirrors
// deploy/pilot/beads-reaper.py: stamp kelos-attempt-<n> per failure and, once
// MaxAttempts is exhausted, apply the blocked label, remove the trigger label
// and say why in a comment.
//
// The trigger label is deliberately left in place below the ceiling — that is
// what allows a retry at all — so this policy is what makes keeping it on
// failure safe. Best-effort like the success path: nothing here fails the Task.
func (tr *TaskReporter) applyFailurePolicy(ctx context.Context, task *kelos.Task, number int) {
	annotations := task.Annotations

	raw, ok := annotations[AnnotationGitHubFailureMaxAttempts]
	if !ok {
		// Policy not configured: preserve the previous unbounded behaviour
		// rather than silently imposing a ceiling on existing spawners.
		return
	}
	maxAttempts, err := strconv.Atoi(raw)
	if err != nil {
		ctrl.Log.WithName("reporter").Error(err, "Ignoring malformed retry-ceiling annotation",
			"task", task.Name, "annotation", AnnotationGitHubFailureMaxAttempts, "value", raw)
		return
	}
	if maxAttempts <= 0 {
		// Explicitly disabled.
		return
	}

	log := ctrl.Log.WithName("reporter")

	prefix := annotations[AnnotationGitHubFailureAttemptLabelPrefix]
	if prefix == "" {
		prefix = DefaultAttemptLabelPrefix
	}
	blockedLabel := annotations[AnnotationGitHubFailureBlockedLabel]
	if blockedLabel == "" {
		blockedLabel = DefaultBlockedLabel
	}
	removeAtCeiling := splitAnnotationList(annotations[AnnotationGitHubFailureRemoveLabels])

	current, err := tr.Reporter.ListLabels(ctx, number)
	if err != nil {
		// Without the current labels the attempt count is unknown. Guessing
		// would either re-stamp attempt 1 for ever (no ceiling) or jump straight
		// to blocked (losing legitimate retries), so do neither.
		log.Error(err, "Failed to list GitHub labels; cannot advance the retry ceiling this cycle",
			"task", task.Name, "number", number)
		return
	}

	attempt := highestAttempt(current, prefix) + 1

	if attempt <= maxAttempts {
		attemptLabel := prefix + strconv.Itoa(attempt)
		if err := tr.Reporter.AddLabels(ctx, number, []string{attemptLabel}); err != nil {
			log.Error(err, "Failed to stamp the failed-attempt label; this failure will not count against the retry ceiling",
				"task", task.Name, "number", number, "label", attemptLabel)
			return
		}
		failureAttemptsRecordedTotal.Inc()
		log.Info("Recorded a failed attempt on the originating GitHub issue",
			"task", task.Name, "number", number, "attempt", attempt, "maxAttempts", maxAttempts, "label", attemptLabel)
		return
	}

	// At the ceiling. alreadyBlocked is read before any write so a re-report of
	// the same phase (a controller restart, a failed annotation persist) does not
	// post a duplicate explanation comment; the label writes are idempotent on
	// GitHub's side and need no such guard.
	alreadyBlocked := containsLabel(current, blockedLabel)

	if err := tr.Reporter.AddLabels(ctx, number, []string{blockedLabel}); err != nil {
		log.Error(err, "Failed to apply the blocked label at the retry ceiling",
			"task", task.Name, "number", number, "label", blockedLabel)
	}

	removed := 0
	for _, label := range removeAtCeiling {
		if err := tr.Reporter.RemoveLabel(ctx, number, label); err != nil {
			labelRemovalsTotal.WithLabelValues("error").Inc()
			log.Error(err, "Failed to remove the GitHub trigger label at the retry ceiling; the issue will keep being rediscovered",
				"task", task.Name, "number", number, "label", label)
			continue
		}
		labelRemovalsTotal.WithLabelValues("success").Inc()
		removed++
	}

	if !alreadyBlocked {
		failureCeilingReachedTotal.Inc()
		body := FormatCeilingReachedComment(task.Name, attempt-1, maxAttempts, blockedLabel, removeAtCeiling)
		if _, err := tr.Reporter.CreateComment(ctx, number, body); err != nil {
			log.Error(err, "Failed to comment the retry-ceiling explanation",
				"task", task.Name, "number", number)
		}
	}

	log.Info("Retry ceiling exhausted for the originating GitHub issue",
		"task", task.Name, "number", number,
		"attempts", attempt-1, "maxAttempts", maxAttempts,
		"blockedLabel", blockedLabel, "labelsRemoved", removed,
		"alreadyBlocked", alreadyBlocked)
}

// highestAttempt returns the largest n across labels of the form
// <prefix><n>, or 0 when there is none. Mirrors beads-reaper.py's
// attempts_so_far.
func highestAttempt(labels []string, prefix string) int {
	re := regexp.MustCompile(`^` + regexp.QuoteMeta(prefix) + `(\d+)$`)
	highest := 0
	for _, label := range labels {
		m := re.FindStringSubmatch(label)
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		if n > highest {
			highest = n
		}
	}
	return highest
}

func containsLabel(labels []string, want string) bool {
	for _, l := range labels {
		if l == want {
			return true
		}
	}
	return false
}

// FormatCeilingReachedComment explains why kelos has stopped retrying an issue.
func FormatCeilingReachedComment(taskName string, attempts, maxAttempts int, blockedLabel string, removedLabels []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "🤖 **Kelos Task Status**\n\nTask `%s` has **failed**, and this issue has reached the retry ceiling "+
		"(%d of %d attempts). ⛔\n\n", taskName, attempts, maxAttempts)
	fmt.Fprintf(&b, "Labelled `%s`", blockedLabel)
	if len(removedLabels) > 0 {
		fmt.Fprintf(&b, " and removed `%s`", strings.Join(removedLabels, "`, `"))
	}
	b.WriteString(", so kelos will not pick this issue up again. Re-apply the trigger label after addressing the cause to retry.")
	return b.String()
}
