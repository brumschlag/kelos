package source

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readyJSON mirrors the shape of `bd ready --json` output.
const readyJSON = `[
  {
    "id": "pain-16c090ee",
    "title": "Fix tenant database creation failures",
    "description": "Backend fails to start with 'database does not exist'.",
    "notes": "Full remediation plan: s3://bucket/triage/pain-16c090ee.md",
    "status": "open",
    "priority": 1,
    "issue_type": "task",
    "labels": ["standup-pain", "triaged"]
  },
  {
    "id": "pain-4ce0eac9",
    "title": "Fix locationId validation error",
    "description": "",
    "notes": "",
    "status": "open",
    "priority": 3,
    "issue_type": "bug",
    "labels": ["triaged"]
  }
]`

// readyMixedJSON mirrors a shared hub: one Dolt database serving several
// projects, which is what the live hub does. `bd ready` reports all of them
// because it has no prefix filter.
const readyMixedJSON = `[
  {
    "id": "pain-16c090ee",
    "title": "Fix tenant database creation failures",
    "issue_type": "task",
    "labels": ["triaged"]
  },
  {
    "id": "beadseed-ugu",
    "title": "Another project's ready bead",
    "issue_type": "task",
    "labels": ["triaged"]
  },
  {
    "id": "painful-abc",
    "title": "A project whose prefix merely starts with ours",
    "issue_type": "task",
    "labels": ["triaged"]
  },
  {
    "id": "pain-4ce0eac9",
    "title": "Fix locationId validation error",
    "issue_type": "bug",
    "labels": ["triaged"]
  }
]`

// fakeCLI records every invocation and replays canned output keyed by
// subcommand, so tests can assert the exact argument vectors the source builds.
type fakeCLI struct {
	calls  [][]string
	output map[string]string
	err    map[string]error
}

func (f *fakeCLI) run(_ context.Context, _ string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, args)
	key := args[0]
	if err, ok := f.err[key]; ok {
		return nil, err
	}
	return []byte(f.output[key]), nil
}

func (f *fakeCLI) call(subcommand string) []string {
	for _, c := range f.calls {
		if c[0] == subcommand {
			return c
		}
	}
	return nil
}

func newBeadsSource(t *testing.T, cli *fakeCLI, cloned bool) *BeadsSource {
	t.Helper()
	t.Setenv(beadsPasswordEnv, "secret")

	dir := t.TempDir()
	if cloned {
		if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0o750); err != nil {
			t.Fatalf("seeding clone: %v", err)
		}
	}

	return &BeadsSource{
		Remote:   "https://beads.example.com:50051/beads",
		Database: "beads",
		Prefix:   "pain",
		WorkDir:  dir,
		Run:      cli.run,
	}
}

func TestBeadsDiscoverMapsReadyIssues(t *testing.T) {
	cli := &fakeCLI{output: map[string]string{"ready": readyJSON}}
	s := newBeadsSource(t, cli, true)

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}

	first := items[0]
	if first.ID != "pain-16c090ee" {
		t.Errorf("ID = %q, want pain-16c090ee", first.ID)
	}
	if first.Title != "Fix tenant database creation failures" {
		t.Errorf("Title = %q", first.Title)
	}
	if !strings.Contains(first.Body, "database does not exist") {
		t.Errorf("Body = %q, want the bead description", first.Body)
	}
	if !strings.Contains(first.Comments, "s3://bucket/triage/pain-16c090ee.md") {
		t.Errorf("Comments = %q, want the bead notes", first.Comments)
	}
	if first.Kind != "task" {
		t.Errorf("Kind = %q, want task", first.Kind)
	}
	if len(first.Labels) != 2 {
		t.Errorf("Labels = %v, want 2 labels", first.Labels)
	}

	// The CLI ranks by priority; the source must not reorder its output.
	if items[1].ID != "pain-4ce0eac9" {
		t.Errorf("second item = %q, want pain-4ce0eac9 (CLI order preserved)", items[1].ID)
	}

	// TriggerTime must stay zero: a bead's updated_at would retrigger a
	// finished Task every time an agent wrote back to its own bead.
	if !first.TriggerTime.IsZero() {
		t.Errorf("TriggerTime = %v, want zero", first.TriggerTime)
	}
}

func TestBeadsDiscoverBuildsReadyArgs(t *testing.T) {
	cli := &fakeCLI{output: map[string]string{"ready": readyJSON}}
	s := newBeadsSource(t, cli, true)
	limit := int32(5)
	s.Labels = []string{"triaged", "standup-pain"}
	s.ExcludeLabels = []string{"dispatched"}
	s.Limit = limit

	if _, err := s.Discover(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := strings.Join(cli.call("ready"), " ")
	want := "ready --json --readonly --sandbox " +
		"--label triaged --label standup-pain --exclude-label dispatched --limit 5"
	if got != want {
		t.Errorf("ready args =\n  %q\nwant\n  %q", got, want)
	}
}

func TestBeadsDiscoverOmitsLimitWhenUnset(t *testing.T) {
	cli := &fakeCLI{output: map[string]string{"ready": readyJSON}}
	s := newBeadsSource(t, cli, true)

	if _, err := s.Discover(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := strings.Join(cli.call("ready"), " "); strings.Contains(got, "--limit") {
		t.Errorf("ready args = %q, want no --limit when Limit is zero", got)
	}
}

func TestBeadsDiscoverPullsExistingClone(t *testing.T) {
	cli := &fakeCLI{output: map[string]string{"ready": readyJSON}}
	s := newBeadsSource(t, cli, true)

	if _, err := s.Discover(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := strings.Join(cli.call("dolt"), " "); got != "dolt pull" {
		t.Errorf("refresh call = %q, want %q", got, "dolt pull")
	}
	if cli.call("init") != nil {
		t.Error("expected no init call when the clone already exists")
	}
}

func TestBeadsDiscoverClonesWhenMissing(t *testing.T) {
	cli := &fakeCLI{output: map[string]string{"ready": readyJSON}}
	s := newBeadsSource(t, cli, false)

	if _, err := s.Discover(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := strings.Join(cli.call("init"), " ")
	want := "init --non-interactive --database beads --prefix pain " +
		"--remote https://beads.example.com:50051/beads"
	if got != want {
		t.Errorf("init args =\n  %q\nwant\n  %q", got, want)
	}

	// Discovery runs unattended, so the CLI's usage metrics are turned off.
	if got := strings.Join(cli.call("metrics"), " "); got != "metrics off" {
		t.Errorf("metrics call = %q, want %q", got, "metrics off")
	}
	if cli.call("dolt") != nil {
		t.Error("expected no dolt pull on the initial clone")
	}
}

func TestBeadsDiscoverRequiresPassword(t *testing.T) {
	cli := &fakeCLI{output: map[string]string{"ready": readyJSON}}
	s := newBeadsSource(t, cli, true)
	t.Setenv(beadsPasswordEnv, "")

	_, err := s.Discover(context.Background())
	if err == nil {
		t.Fatal("expected an error when the password env var is empty")
	}
	if !strings.Contains(err.Error(), beadsPasswordEnv) {
		t.Errorf("error = %v, want it to name %s", err, beadsPasswordEnv)
	}
	if len(cli.calls) != 0 {
		t.Errorf("expected no CLI calls, got %v", cli.calls)
	}
}

// TestBeadsDiscoverScopesToPrefix covers what the prefix field is documented to
// do. `bd ready` accepts no prefix filter, so a clone initialized with one still
// reports every project sharing the database — the live hub serves 363 "pain-",
// 65 "beadseed-" and 13 "brian-" beads out of one. Without this filter the only
// thing scoping a spawner is its optional label list.
func TestBeadsDiscoverScopesToPrefix(t *testing.T) {
	cli := &fakeCLI{output: map[string]string{"ready": readyMixedJSON}}
	s := newBeadsSource(t, cli, true)

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var got []string
	for _, item := range items {
		got = append(got, item.ID)
	}
	want := []string{"pain-16c090ee", "pain-4ce0eac9"}
	if len(got) != len(want) {
		t.Fatalf("IDs = %v, want only the %q-prefixed beads %v", got, s.Prefix, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("IDs = %v, want %v (CLI order preserved)", got, want)
			break
		}
	}
}

// TestBeadsDiscoverPrefixRequiresSeparator pins the separator to the match. A
// bare strings.HasPrefix on "pain" would also accept a "painful-" project, which
// is a different tracker in the same database.
func TestBeadsDiscoverPrefixRequiresSeparator(t *testing.T) {
	cli := &fakeCLI{output: map[string]string{"ready": readyMixedJSON}}
	s := newBeadsSource(t, cli, true)

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, item := range items {
		if item.ID == "painful-abc" {
			t.Errorf("ID %q was accepted for prefix %q; the %q separator must be part of the match",
				item.ID, s.Prefix, "-")
		}
	}
}

// TestBeadsDiscoverRequiresPrefix guards the fail-fast: an empty prefix widens
// discovery to every project in the hub instead of narrowing it, so it must be
// rejected rather than treated as "no filter".
func TestBeadsDiscoverRequiresPrefix(t *testing.T) {
	cli := &fakeCLI{output: map[string]string{"ready": readyMixedJSON}}
	s := newBeadsSource(t, cli, true)
	s.Prefix = ""

	_, err := s.Discover(context.Background())
	if err == nil {
		t.Fatal("expected an error when Prefix is empty")
	}
	if !strings.Contains(err.Error(), "prefix") {
		t.Errorf("error = %v, want it to name the prefix", err)
	}
	if len(cli.calls) != 0 {
		t.Errorf("expected no CLI calls before the config check, got %v", cli.calls)
	}
}

func TestBeadsDiscoverRequiresWorkDir(t *testing.T) {
	t.Setenv(beadsPasswordEnv, "secret")
	s := &BeadsSource{Remote: "https://beads.example.com:50051/beads"}

	if _, err := s.Discover(context.Background()); err == nil {
		t.Fatal("expected an error when WorkDir is empty")
	}
}

func TestBeadsDiscoverSurfacesCLIFailure(t *testing.T) {
	cli := &fakeCLI{
		output: map[string]string{"ready": readyJSON},
		err:    map[string]error{"dolt": errors.New("remote unreachable")},
	}
	s := newBeadsSource(t, cli, true)

	_, err := s.Discover(context.Background())
	if err == nil {
		t.Fatal("expected the pull failure to surface")
	}
	if !strings.Contains(err.Error(), "remote unreachable") {
		t.Errorf("error = %v, want it to wrap the CLI failure", err)
	}
}

func TestBeadsDiscoverRejectsInvalidJSON(t *testing.T) {
	cli := &fakeCLI{output: map[string]string{"ready": "not json"}}
	s := newBeadsSource(t, cli, true)

	if _, err := s.Discover(context.Background()); err == nil {
		t.Fatal("expected a decode error for malformed CLI output")
	}
}

func TestBeadsDiscoverEmptyResult(t *testing.T) {
	cli := &fakeCLI{output: map[string]string{"ready": "[]"}}
	s := newBeadsSource(t, cli, true)

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("expected no items, got %d", len(items))
	}
}

// TestBeadsDiscoverRetriesAfterFailedClone covers the wedge found running this
// source in a cluster: the CLI creates .beads before it fails to reach the
// remote, so a naive "does .beads exist?" check sends every later cycle down
// the pull path, which fails with "no remote" forever.
func TestBeadsDiscoverRetriesAfterFailedClone(t *testing.T) {
	t.Setenv(beadsPasswordEnv, "secret")
	dir := t.TempDir()

	var calls [][]string
	failInit := true
	s := &BeadsSource{
		Remote:   "https://beads.example.com:50051/beads",
		Database: "beads",
		Prefix:   "pain",
		WorkDir:  dir,
		Run: func(_ context.Context, workDir string, args ...string) ([]byte, error) {
			calls = append(calls, args)
			if args[0] == "init" {
				// The real CLI leaves this behind even when the clone fails.
				if err := os.MkdirAll(filepath.Join(workDir, ".beads"), 0o700); err != nil {
					t.Fatalf("seeding partial clone: %v", err)
				}
				if failInit {
					return nil, errors.New("could not access dolt url: i/o timeout")
				}
				return nil, nil
			}
			if args[0] == "ready" {
				return []byte(readyJSON), nil
			}
			return nil, nil
		},
	}

	if _, err := s.Discover(context.Background()); err == nil {
		t.Fatal("expected the failed clone to surface as an error")
	}

	// The partial clone must not survive, or the next cycle pulls from a
	// repository that has no remote configured.
	if _, err := os.Stat(filepath.Join(dir, ".beads")); !os.IsNotExist(err) {
		t.Fatalf("expected the partial clone to be discarded, stat err = %v", err)
	}

	// Second cycle: the remote is reachable now, so it must retry init.
	failInit = false
	calls = nil
	if _, err := s.Discover(context.Background()); err != nil {
		t.Fatalf("expected recovery on the next cycle, got %v", err)
	}

	var sawInit, sawPull bool
	for _, c := range calls {
		switch {
		case c[0] == "init":
			sawInit = true
		case c[0] == "dolt" && len(c) > 1 && c[1] == "pull":
			sawPull = true
		}
	}
	if !sawInit {
		t.Errorf("expected a retried init, got calls %v", calls)
	}
	if sawPull {
		t.Errorf("expected no pull against a never-completed clone, got calls %v", calls)
	}
}
