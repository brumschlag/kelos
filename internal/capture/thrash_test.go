package capture

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func envMap(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestLoadThrashConfig(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    thrashConfig
		wantErr bool
	}{
		{
			name: "defaults stop at the first repeat within 3 turns",
			env:  map[string]string{},
			want: thrashConfig{MaxRepeats: 1, WindowTurns: 3},
		},
		{
			name: "explicit values and pid file",
			env: map[string]string{
				envThrashMaxRepeats:  "2",
				envThrashWindowTurns: "5",
				envAgentPIDFile:      "/tmp/agent.pid",
			},
			want: thrashConfig{MaxRepeats: 2, WindowTurns: 5, AgentPIDFile: "/tmp/agent.pid"},
		},
		{
			name: "zero max repeats disables",
			env:  map[string]string{envThrashMaxRepeats: "0"},
			want: thrashConfig{MaxRepeats: 0, WindowTurns: 3},
		},
		{
			name: "zero window disables",
			env:  map[string]string{envThrashWindowTurns: "0"},
			want: thrashConfig{MaxRepeats: 1, WindowTurns: 0},
		},
		{name: "non-numeric max repeats", env: map[string]string{envThrashMaxRepeats: "abc"}, wantErr: true},
		{name: "negative window", env: map[string]string{envThrashWindowTurns: "-1"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := loadThrashConfig(envMap(tt.env))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Expected error, got config %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("Config = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestNewThrashDetectorDisabled(t *testing.T) {
	for _, cfg := range []thrashConfig{
		{MaxRepeats: 0, WindowTurns: 3},
		{MaxRepeats: 1, WindowTurns: 0},
	} {
		if d := newThrashDetector(cfg); d != nil {
			t.Errorf("newThrashDetector(%+v) = %+v, want nil", cfg, d)
		}
	}
}

// Stream-json line builders for detector tests.
func assistantLine(id string) string {
	return `{"type":"assistant","message":{"id":"` + id + `","content":[{"type":"tool_use","name":"Read"}]}}`
}

func compactLine(trigger string, preTokens int) string {
	return `{"type":"system","subtype":"compact_boundary","compact_metadata":{"trigger":"` + trigger + `","pre_tokens":` + strconv.Itoa(preTokens) + `}}`
}

// feed sends lines to the detector and returns the 1-based index of the
// line that tripped it, or 0 if it never tripped.
func feed(d *thrashDetector, lines ...string) int {
	for i, line := range lines {
		if d.addLine([]byte(line)) {
			return i + 1
		}
	}
	return 0
}

func TestThrashDetectorSingleCompactionDoesNotTrigger(t *testing.T) {
	d := newThrashDetector(thrashConfig{MaxRepeats: 1, WindowTurns: 3})
	lines := []string{assistantLine("m1"), assistantLine("m2"), compactLine("auto", 167000)}
	for i := 3; i < 50; i++ {
		lines = append(lines, assistantLine("m"+strconv.Itoa(i)))
	}
	if got := feed(d, lines...); got != 0 {
		t.Fatalf("Detector tripped on line %d for a single compaction", got)
	}
}

func TestThrashDetectorTripsOnFirstRepeatWithinWindow(t *testing.T) {
	d := newThrashDetector(thrashConfig{MaxRepeats: 1, WindowTurns: 3})
	got := feed(d,
		assistantLine("m1"),
		compactLine("auto", 169394),
		assistantLine("m2"),
		assistantLine("m2"), // same API response, second content block
		assistantLine("m3"),
		compactLine("auto", 169113),
		assistantLine("m4"),
	)
	if got != 6 {
		t.Fatalf("Detector tripped on line %d, want 6 (the second auto compaction)", got)
	}
	msg := d.report()
	for _, want := range []string{"autocompact thrash", "2 auto-compactions", "2 assistant turns", "169394", "169113"} {
		if !strings.Contains(msg, want) {
			t.Errorf("Report %q does not contain %q", msg, want)
		}
	}
}

func TestThrashDetectorCompactionAtWindowBoundaryDoesNotTrigger(t *testing.T) {
	// Claude Code's own breaker treats "within 3 turns" as fewer than 3; a
	// refill that took exactly 3 turns is not a repeat.
	d := newThrashDetector(thrashConfig{MaxRepeats: 1, WindowTurns: 3})
	got := feed(d,
		compactLine("auto", 167000),
		assistantLine("m1"),
		assistantLine("m2"),
		assistantLine("m3"),
		compactLine("auto", 167000),
	)
	if got != 0 {
		t.Fatalf("Detector tripped on line %d for a compaction exactly WindowTurns turns later", got)
	}
}

func TestThrashDetectorIgnoresManualCompactions(t *testing.T) {
	d := newThrashDetector(thrashConfig{MaxRepeats: 1, WindowTurns: 3})
	got := feed(d,
		compactLine("manual", 100000),
		assistantLine("m1"),
		compactLine("manual", 100000),
		assistantLine("m2"),
		compactLine("auto", 167000),
		assistantLine("m3"),
		compactLine("manual", 100000),
		assistantLine("m4"),
		assistantLine("m5"),
		assistantLine("m6"),
		compactLine("auto", 167000),
	)
	if got != 0 {
		t.Fatalf("Detector tripped on line %d; manual compactions must not count", got)
	}
}

func TestThrashDetectorMaxRepeatsRequiresConsecutiveRepeats(t *testing.T) {
	d := newThrashDetector(thrashConfig{MaxRepeats: 2, WindowTurns: 3})
	got := feed(d,
		compactLine("auto", 1),
		assistantLine("m1"),
		compactLine("auto", 2), // repeat 1
		assistantLine("m2"),
		assistantLine("m3"),
		assistantLine("m4"),
		compactLine("auto", 3), // not a repeat: resets
		assistantLine("m5"),
		compactLine("auto", 4), // repeat 1
		assistantLine("m6"),
		compactLine("auto", 5), // repeat 2: trip
	)
	if got != 11 {
		t.Fatalf("Detector tripped on line %d, want 11", got)
	}
}

func TestThrashDetectorTripsOnlyOnce(t *testing.T) {
	d := newThrashDetector(thrashConfig{MaxRepeats: 1, WindowTurns: 3})
	lines := []string{compactLine("auto", 1), compactLine("auto", 2), compactLine("auto", 3)}
	trips := 0
	for _, line := range lines {
		if d.addLine([]byte(line)) {
			trips++
		}
	}
	if trips != 1 {
		t.Fatalf("Detector tripped %d times, want 1", trips)
	}
}

func TestThrashDetectorIgnoresNonJSONLines(t *testing.T) {
	d := newThrashDetector(thrashConfig{MaxRepeats: 1, WindowTurns: 3})
	got := feed(d,
		"kelos-capture: compact_boundary mentioned in plain stderr",
		`not json "assistant"`,
		compactLine("auto", 1),
		"plain compact_boundary text",
	)
	if got != 0 {
		t.Fatalf("Detector tripped on line %d from non-JSON input", got)
	}
}

// Backtests against trimmed real pod logs (see testdata/). Claude Code's own
// breaker ended each thrash run; the detector must stop them far earlier and
// must not stop the run that recovered and succeeded.
func TestThrashDetectorBacktest(t *testing.T) {
	tests := []struct {
		fixture          string
		wantTrip         bool
		wantCompactions  int
		wantPreTokensIn  []string
		wantAutoCompacts int
	}{
		{
			fixture:          "thrash-issue-5527-qpvwl.jsonl",
			wantTrip:         true,
			wantCompactions:  2,
			wantPreTokensIn:  []string{"169394", "169113"},
			wantAutoCompacts: 3,
		},
		{
			// 23 compactions and $28 before Claude Code gave up.
			fixture:          "thrash-issue-5478-clhgn.jsonl",
			wantTrip:         true,
			wantCompactions:  4,
			wantPreTokensIn:  []string{"170039", "167346", "170583", "166728"},
			wantAutoCompacts: 23,
		},
		{
			// Six compactions, none within 3 turns of the previous: the run
			// succeeded and opened a PR.
			fixture:          "no-thrash-issue-5526-8wdxc.jsonl",
			wantTrip:         false,
			wantAutoCompacts: 6,
		},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			f, err := os.Open(filepath.Join("testdata", tt.fixture))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()

			d := newThrashDetector(thrashConfig{MaxRepeats: 1, WindowTurns: 3})
			tripped := false
			autoCompacts := 0
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 1024*1024), 1024*1024)
			for sc.Scan() {
				if strings.Contains(sc.Text(), `"trigger":"auto"`) {
					autoCompacts++
				}
				if d.addLine(sc.Bytes()) {
					tripped = true
					break
				}
			}
			if err := sc.Err(); err != nil {
				t.Fatal(err)
			}
			if !tripped {
				// Count the rest of the file so the fixture sanity check below
				// covers the whole stream.
				for sc.Scan() {
					if strings.Contains(sc.Text(), `"trigger":"auto"`) {
						autoCompacts++
					}
				}
			}
			if tripped != tt.wantTrip {
				t.Fatalf("Tripped = %v, want %v", tripped, tt.wantTrip)
			}
			if !tt.wantTrip {
				if autoCompacts != tt.wantAutoCompacts {
					t.Fatalf("Fixture has %d auto compactions, want %d", autoCompacts, tt.wantAutoCompacts)
				}
				return
			}
			if len(d.compactions) != tt.wantCompactions {
				t.Fatalf("Stopped after %d compactions, want %d", len(d.compactions), tt.wantCompactions)
			}
			msg := d.report()
			for _, want := range tt.wantPreTokensIn {
				if !strings.Contains(msg, want) {
					t.Errorf("Report %q does not contain pre_tokens %s", msg, want)
				}
			}
		})
	}
}

type fakeStopper struct {
	calls  int
	err    error
	cancel int
}

func (s *fakeStopper) stop() (func(), error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return func() { s.cancel++ }, nil
}

func TestRunStopsAgentOnAutocompactThrash(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "thrash-issue-5527-qpvwl.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	// Drop Claude Code's own result line: the agent is stopped before it
	// would have produced one.
	idx := bytes.Index(data, []byte(`{"type":"result"`))
	if idx < 0 {
		t.Fatal("fixture has no result line")
	}
	input := data[:idx]

	var stdout, stderr bytes.Buffer
	stopper := &fakeStopper{}
	code := run("claude-code", bytes.NewReader(input), &stdout, &stderr, mockRunner{}, thrashConfig{MaxRepeats: 1, WindowTurns: 3}, stopper)

	// Exactly ExitThrashStopped, not merely non-zero: the controller's default
	// podFailurePolicy FailJob codes include it, so a thrash stop fails the Job
	// instead of being retried however Claude Code itself exited.
	if code != ExitThrashStopped {
		t.Fatalf("Exit code = %d, want %d (ExitThrashStopped) so the Job fails without a retry", code, ExitThrashStopped)
	}
	if stopper.calls != 1 {
		t.Fatalf("Stopper called %d times, want 1", stopper.calls)
	}
	if stopper.cancel != 1 {
		t.Fatalf("Stop cancel called %d times, want 1 once the stream ended", stopper.cancel)
	}
	out := stdout.String()
	if !strings.HasPrefix(out, string(input)) {
		t.Fatal("Stream was not forwarded unchanged before the outputs block")
	}
	response := outputValue(t, out, "response")
	decoded, err := base64.StdEncoding.DecodeString(response)
	if err != nil {
		t.Fatalf("Decoding response %q: %v", response, err)
	}
	for _, want := range []string{"Stopped early by kelos-capture", "autocompact thrash", "169394", "169113"} {
		if !strings.Contains(string(decoded), want) {
			t.Errorf("Response %q does not contain %q", decoded, want)
		}
	}
	if !strings.Contains(stderr.String(), "autocompact thrash") {
		t.Errorf("Stderr %q does not mention the thrash stop", stderr.String())
	}
}

func TestRunOverridesResponseWhenAgentWritesResultAfterStop(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "thrash-issue-5527-qpvwl.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run("claude-code", bytes.NewReader(data), &stdout, &stderr, mockRunner{}, thrashConfig{MaxRepeats: 1, WindowTurns: 3}, &fakeStopper{})
	if code != ExitThrashStopped {
		t.Fatalf("Exit code = %d, want %d (ExitThrashStopped)", code, ExitThrashStopped)
	}
	decoded, _ := base64.StdEncoding.DecodeString(outputValue(t, stdout.String(), "response"))
	if !strings.Contains(string(decoded), "Stopped early by kelos-capture") {
		t.Fatalf("Response = %q, want the kelos-capture thrash message", decoded)
	}
	if got := outputValue(t, stdout.String(), "cost-usd"); got != "3.9937039999999997" {
		t.Fatalf("cost-usd = %q, want the agent-reported cost to be kept", got)
	}
}

func TestRunStopsReadingWhenAgentCannotBeSignalled(t *testing.T) {
	stream := strings.Join([]string{
		compactLine("auto", 1),
		compactLine("auto", 2),
		assistantLine("after-stop"),
	}, "\n") + "\n"
	var stdout, stderr bytes.Buffer
	stopper := &fakeStopper{err: errors.New("no pid file")}
	code := run("claude-code", strings.NewReader(stream), &stdout, &stderr, mockRunner{}, thrashConfig{MaxRepeats: 1, WindowTurns: 3}, stopper)
	if code == 0 {
		t.Fatal("Exit code = 0, want non-zero")
	}
	if strings.Contains(stdout.String(), "after-stop") {
		t.Fatal("Stream kept being consumed after the agent could not be stopped")
	}
	if !strings.Contains(stderr.String(), "no pid file") {
		t.Fatalf("Stderr %q does not report the stop failure", stderr.String())
	}
	if outputValue(t, stdout.String(), "response") == "" {
		t.Fatal("Missing response output")
	}
}

func TestRunDoesNotDetectThrashForOtherAgents(t *testing.T) {
	stream := compactLine("auto", 1) + "\n" + compactLine("auto", 2) + "\n"
	var stdout, stderr bytes.Buffer
	stopper := &fakeStopper{}
	run("codex", strings.NewReader(stream), &stdout, &stderr, mockRunner{}, thrashConfig{MaxRepeats: 1, WindowTurns: 3}, stopper)
	if stopper.calls != 0 {
		t.Fatalf("Stopper called %d times for a non-Claude agent", stopper.calls)
	}
}

func TestRunDisabledThrashDetection(t *testing.T) {
	stream := compactLine("auto", 1) + "\n" + compactLine("auto", 2) + "\n"
	var stdout, stderr bytes.Buffer
	stopper := &fakeStopper{}
	code := run("claude-code", strings.NewReader(stream), &stdout, &stderr, mockRunner{}, thrashConfig{MaxRepeats: 0, WindowTurns: 3}, stopper)
	if stopper.calls != 0 {
		t.Fatalf("Stopper called %d times with detection disabled", stopper.calls)
	}
	if code != 0 {
		t.Fatalf("Exit code = %d, want 0", code)
	}
}

func TestPIDFileStopperSendsSIGTERM(t *testing.T) {
	agent := exec.Command("sleep", "30")
	if err := agent.Start(); err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(t.TempDir(), "agent.pid")
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(agent.Process.Pid)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cancel, err := pidFileStopper{path: pidFile, grace: time.Minute}.stop()
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- agent.Wait() }()
	select {
	case err := <-done:
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("Wait error = %v, want an ExitError", err)
		}
		status, ok := exitErr.Sys().(syscall.WaitStatus)
		if !ok || !status.Signaled() || status.Signal() != syscall.SIGTERM {
			t.Fatalf("Agent exit status = %v, want terminated by SIGTERM", exitErr)
		}
	case <-time.After(10 * time.Second):
		_ = agent.Process.Kill()
		t.Fatal("Agent was not stopped")
	}
}

func TestPIDFileStopperErrors(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.pid")
	if err := os.WriteFile(bad, []byte("not-a-pid"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"unset":   "",
		"missing": filepath.Join(dir, "missing.pid"),
		"invalid": bad,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := (pidFileStopper{path: path}).stop(); err == nil {
				t.Fatal("Expected error")
			}
		})
	}
}

func outputValue(t *testing.T, out, key string) string {
	t.Helper()
	start := strings.LastIndex(out, markerStart)
	end := strings.LastIndex(out, markerEnd)
	if start < 0 || end < start {
		t.Fatalf("No outputs block in %q", out)
	}
	for _, line := range strings.Split(out[start:end], "\n") {
		if v, ok := strings.CutPrefix(line, key+": "); ok {
			return v
		}
	}
	return ""
}
