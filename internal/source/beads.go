package source

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	// beadsCLI is the beads command the spawner image provides on PATH.
	beadsCLI = "bd"

	// beadsPasswordEnv is the environment variable the beads CLI reads the
	// Dolt remote password from. The spawner Deployment projects it from the
	// Secret named by when.beads.secretRef.
	beadsPasswordEnv = "BEADS_DOLT_PASSWORD"
)

// BeadsSource discovers ready work from a beads tracker by driving the beads
// CLI against a local clone of the Dolt remote.
//
// Readiness is whatever `bd ready` reports: open issues with no active
// blockers, excluding in-progress, blocked, deferred, hooked, and ephemeral
// ones. That set is deliberately not re-derived here — the CLI owns those
// semantics, and reimplementing them would let the two drift apart silently.
type BeadsSource struct {
	// Remote is the Dolt remote URL of the beads hub.
	Remote string

	// Database is the Dolt database name served by the remote.
	Database string

	// Prefix scopes discovery to one project's bead IDs within a shared hub.
	Prefix string

	// Labels restricts discovery to issues carrying ALL of these labels.
	Labels []string

	// ExcludeLabels skips issues carrying ANY of these labels.
	ExcludeLabels []string

	// Limit caps how many ready issues one cycle returns. Zero leaves the
	// CLI's own default in place.
	Limit int32

	// WorkDir holds the clone. It must be writable: the CLI keeps the Dolt
	// database under WorkDir/.beads and auto-starts a local server there.
	WorkDir string

	// Run executes the beads CLI. Tests replace it; when nil the real CLI runs.
	Run func(ctx context.Context, dir string, args ...string) ([]byte, error)
}

// beadsIssue is the subset of `bd ready --json` output the spawner consumes.
type beadsIssue struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Notes       string   `json:"notes"`
	IssueType   string   `json:"issue_type"`
	Labels      []string `json:"labels"`
}

func (s *BeadsSource) run(ctx context.Context, args ...string) ([]byte, error) {
	if s.Run != nil {
		return s.Run(ctx, s.WorkDir, args...)
	}
	return runBeadsCLI(ctx, s.WorkDir, args...)
}

// runBeadsCLI invokes the beads CLI in dir, surfacing stderr in the error so a
// CLI failure is diagnosable from the spawner log.
func runBeadsCLI(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, beadsCLI, args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("%s %s: %w: %s", beadsCLI, strings.Join(args, " "), err, msg)
		}
		return nil, fmt.Errorf("%s %s: %w", beadsCLI, strings.Join(args, " "), err)
	}
	return out, nil
}

// Discover refreshes the local clone and returns the ready beads as WorkItems.
func (s *BeadsSource) Discover(ctx context.Context) ([]WorkItem, error) {
	// Fail loudly rather than let the CLI attempt an unauthenticated clone: a
	// missing password is a configuration error, not a transient one.
	if os.Getenv(beadsPasswordEnv) == "" {
		return nil, fmt.Errorf("%s is not set: the Secret named by when.beads.secretRef must provide it", beadsPasswordEnv)
	}
	if s.WorkDir == "" {
		return nil, fmt.Errorf("beads source requires a writable workDir")
	}

	if err := s.sync(ctx); err != nil {
		return nil, err
	}

	// --readonly blocks writes and --sandbox disables Dolt auto-push, so
	// discovery cannot mutate the shared hub.
	args := []string{"ready", "--json", "--readonly", "--sandbox"}
	for _, label := range s.Labels {
		args = append(args, "--label", label)
	}
	for _, label := range s.ExcludeLabels {
		args = append(args, "--exclude-label", label)
	}
	if s.Limit > 0 {
		args = append(args, "--limit", strconv.Itoa(int(s.Limit)))
	}

	out, err := s.run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("listing ready beads: %w", err)
	}

	var issues []beadsIssue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("decoding beads ready output: %w", err)
	}

	// The CLI already orders by priority. The order it returns is preserved
	// here; sorting by label priority would override the tracker's ranking.
	//
	// TriggerTime is deliberately left unset. The only candidate signal is the
	// bead's updated_at, and using it would retrigger a finished Task every
	// time an agent wrote back to the bead it had just completed.
	items := make([]WorkItem, 0, len(issues))
	for _, issue := range issues {
		items = append(items, WorkItem{
			ID:    issue.ID,
			Title: issue.Title,
			Body:  issue.Description,
			// Notes carry triage output such as remediation-plan pointers,
			// the closest beads analogue to issue comments.
			Comments: issue.Notes,
			Labels:   issue.Labels,
			Kind:     issue.IssueType,
		})
	}

	return items, nil
}

// sync clones the remote on first use and pulls on every later cycle.
func (s *BeadsSource) sync(ctx context.Context) error {
	_, err := os.Stat(filepath.Join(s.WorkDir, ".beads"))
	switch {
	case err == nil:
		if _, err := s.run(ctx, "dolt", "pull"); err != nil {
			return fmt.Errorf("pulling beads remote: %w", err)
		}
		return nil
	case !os.IsNotExist(err):
		return fmt.Errorf("inspecting beads workDir %s: %w", s.WorkDir, err)
	}

	if err := os.MkdirAll(s.WorkDir, 0o750); err != nil {
		return fmt.Errorf("creating beads workDir %s: %w", s.WorkDir, err)
	}

	// First-time setup is treated as one unit. A failed clone still leaves a
	// partial .beads behind, which the next cycle would mistake for a finished
	// clone and try to pull from — failing with "no remote" forever, so an
	// unreachable hub at startup would wedge the source permanently instead of
	// retrying. Discarding the partial state keeps the retry honest.
	if err := s.setUp(ctx); err != nil {
		if rmErr := os.RemoveAll(filepath.Join(s.WorkDir, ".beads")); rmErr != nil {
			return fmt.Errorf("%w (discarding the partial clone also failed: %v)", err, rmErr)
		}
		return err
	}

	return nil
}

// setUp clones the remote and turns off the CLI's usage metrics. Its caller
// discards the working directory if any step fails.
func (s *BeadsSource) setUp(ctx context.Context) error {
	if _, err := s.run(ctx, "init", "--non-interactive",
		"--database", s.Database, "--prefix", s.Prefix, "--remote", s.Remote); err != nil {
		return fmt.Errorf("initializing beads clone from %s: %w", s.Remote, err)
	}

	// Opt out of the CLI's anonymous usage metrics. A spawner polls
	// unattended, so leaving them on would emit telemetry on every cycle.
	if _, err := s.run(ctx, "metrics", "off"); err != nil {
		return fmt.Errorf("disabling beads metrics: %w", err)
	}

	return nil
}
