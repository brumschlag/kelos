package capture

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	// envThrashMaxRepeats is the number of consecutive repeat auto-compactions
	// (each within the turn window of the previous one) that stops Claude
	// Code. 0 disables detection.
	envThrashMaxRepeats = "KELOS_THRASH_MAX_REPEATS"
	// envThrashWindowTurns is the assistant-turn window: an auto-compaction
	// that follows the previous one after fewer than this many assistant turns
	// is a repeat. 0 disables detection.
	envThrashWindowTurns = "KELOS_THRASH_WINDOW_TURNS"
	// envAgentPIDFile names a file holding the PID of the agent process that
	// writes the stream, so kelos-capture can stop it.
	envAgentPIDFile = "KELOS_AGENT_PID_FILE"

	defaultThrashMaxRepeats  = 1
	defaultThrashWindowTurns = 3

	// agentStopGrace is how long the agent has to exit after SIGTERM before
	// it is sent SIGKILL.
	agentStopGrace = 30 * time.Second
)

// thrashConfig configures autocompact thrash detection.
type thrashConfig struct {
	MaxRepeats   int
	WindowTurns  int
	AgentPIDFile string
}

// loadThrashConfig reads the thrash detection settings from the environment.
// It is the only place these variables are read.
func loadThrashConfig(getenv func(string) string) (thrashConfig, error) {
	cfg := thrashConfig{
		MaxRepeats:   defaultThrashMaxRepeats,
		WindowTurns:  defaultThrashWindowTurns,
		AgentPIDFile: getenv(envAgentPIDFile),
	}
	for _, setting := range []struct {
		name string
		dst  *int
	}{
		{envThrashMaxRepeats, &cfg.MaxRepeats},
		{envThrashWindowTurns, &cfg.WindowTurns},
	} {
		raw := strings.TrimSpace(getenv(setting.name))
		if raw == "" {
			continue
		}
		v, err := strconv.Atoi(raw)
		if err != nil || v < 0 {
			return thrashConfig{}, fmt.Errorf("invalid %s %q: must be a non-negative integer", setting.name, raw)
		}
		*setting.dst = v
	}
	return cfg, nil
}

// thrashDetector watches a Claude Code stream-json stream for autocompact
// thrash: an auto-compaction that follows the previous one after fewer than
// WindowTurns assistant turns, MaxRepeats times in a row. This mirrors Claude
// Code's own breaker ("refilled to the limit within 3 turns of the previous
// compact, 3 times in a row"), which gives up only after the third repeat.
// Manual compactions are ignored.
type thrashDetector struct {
	cfg thrashConfig
	// turns counts assistant API responses since the last auto-compaction.
	// Claude Code emits one stream line per content block, so consecutive
	// lines with the same message id are a single turn.
	turns         int
	lastMessageID string
	// compactions holds pre_tokens of every auto-compaction seen so far.
	compactions []int64
	// turnGaps holds the turn count between each auto-compaction and the
	// previous one.
	turnGaps []int
	repeats  int
	tripped  bool
}

// newThrashDetector returns nil when detection is disabled.
func newThrashDetector(cfg thrashConfig) *thrashDetector {
	if cfg.MaxRepeats <= 0 || cfg.WindowTurns <= 0 {
		return nil
	}
	return &thrashDetector{cfg: cfg}
}

var (
	compactBoundaryMarker = []byte(`compact_boundary`)
	assistantMarker       = []byte(`"assistant"`)
)

// addLine consumes one stream line and reports whether this line tripped the
// detector. It returns true at most once.
func (d *thrashDetector) addLine(line []byte) bool {
	if d.tripped {
		return false
	}
	isCompact := bytes.Contains(line, compactBoundaryMarker)
	if !isCompact && !bytes.Contains(line, assistantMarker) {
		return false
	}
	m := parseLine(line)
	if m == nil {
		return false
	}
	switch {
	case m["type"] == "assistant":
		id := ""
		if msg, ok := m["message"].(map[string]any); ok {
			id, _ = msg["id"].(string)
		}
		if id == "" || id != d.lastMessageID {
			d.turns++
		}
		d.lastMessageID = id
	case m["type"] == "system" && m["subtype"] == "compact_boundary":
		meta, _ := m["compact_metadata"].(map[string]any)
		if trigger, _ := meta["trigger"].(string); trigger != "auto" {
			return false
		}
		if len(d.compactions) > 0 {
			d.turnGaps = append(d.turnGaps, d.turns)
			if d.turns < d.cfg.WindowTurns {
				d.repeats++
			} else {
				d.repeats = 0
			}
		}
		d.compactions = append(d.compactions, toInt64(meta["pre_tokens"]))
		d.turns = 0
		d.lastMessageID = ""
		if d.repeats >= d.cfg.MaxRepeats {
			d.tripped = true
			return true
		}
	}
	return false
}

// report describes why the run was stopped. It is used as the Task's
// response output, so it must stand on its own in a status comment.
func (d *thrashDetector) report() string {
	pre := make([]string, len(d.compactions))
	for i, v := range d.compactions {
		pre[i] = strconv.FormatInt(v, 10)
	}
	gaps := make([]string, len(d.turnGaps))
	for i, v := range d.turnGaps {
		gaps[i] = strconv.Itoa(v)
	}
	lastGap := 0
	if len(d.turnGaps) > 0 {
		lastGap = d.turnGaps[len(d.turnGaps)-1]
	}
	return fmt.Sprintf("Stopped early by kelos-capture: autocompact thrash. "+
		"Claude Code ran %d auto-compactions; the last one came %d assistant turns after the previous one "+
		"(repeat threshold: fewer than %d turns, %d time(s) in a row). "+
		"pre_tokens per compaction: %s. Turns between compactions: %s. "+
		"The context refills to the limit almost immediately after compacting, so the run cannot make progress. "+
		"The baseline context (auto-loaded memory files, the prompt) or a large tool output likely leaves too little headroom for the model's context window. "+
		"Set %s=0 to disable this check.",
		len(d.compactions), lastGap, d.cfg.WindowTurns, d.cfg.MaxRepeats,
		strings.Join(pre, ", "), strings.Join(gaps, ", "), envThrashMaxRepeats)
}

// agentStopper stops the agent process that writes the stream. The returned
// cancel func is called once the stream has ended, to cancel any pending
// forced kill.
type agentStopper interface {
	stop() (cancel func(), err error)
}

// pidFileStopper sends SIGTERM to the PID recorded in path, then SIGKILL
// after grace unless cancelled first.
type pidFileStopper struct {
	path  string
	grace time.Duration
}

func (s pidFileStopper) stop() (func(), error) {
	if s.path == "" {
		return nil, fmt.Errorf("%s is not set", envAgentPIDFile)
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return nil, fmt.Errorf("reading agent PID file: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 1 {
		return nil, fmt.Errorf("invalid agent PID %q in %s", strings.TrimSpace(string(data)), s.path)
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return nil, fmt.Errorf("finding agent process %d: %w", pid, err)
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		if errors.Is(err, os.ErrProcessDone) {
			return func() {}, nil
		}
		return nil, fmt.Errorf("sending SIGTERM to agent process %d: %w", pid, err)
	}
	timer := time.AfterFunc(s.grace, func() { _ = proc.Kill() })
	return func() { timer.Stop() }, nil
}
