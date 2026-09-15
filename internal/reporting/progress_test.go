package reporting

import (
	"strings"
	"testing"
)

func TestExtractLatestAssistantText_ClaudeCode(t *testing.T) {
	logs := strings.Join([]string{
		`{"type":"system","subtype":"init","model":"claude-sonnet-4-20250514"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Looking at the configuration files..."}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Found the issue in the auth middleware."}]}}`,
	}, "\n")

	got := ExtractLatestAssistantText(strings.NewReader(logs), "claude-code")
	want := "Found the issue in the auth middleware."
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExtractLatestAssistantText_ClaudeCodeMultipleTextBlocks(t *testing.T) {
	logs := `{"type":"assistant","message":{"content":[{"type":"text","text":"First part."},{"type":"tool_use","name":"Read"},{"type":"text","text":"Second part."}]}}`

	got := ExtractLatestAssistantText(strings.NewReader(logs), "claude-code")
	want := "First part.\nSecond part."
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExtractLatestAssistantText_ClaudeCodeIgnoresToolUseOnly(t *testing.T) {
	logs := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash"}]}}`

	got := ExtractLatestAssistantText(strings.NewReader(logs), "claude-code")
	if got != "" {
		t.Errorf("expected empty string for tool-use-only turn, got %q", got)
	}
}

func TestExtractLatestAssistantText_EmptyLogs(t *testing.T) {
	got := ExtractLatestAssistantText(strings.NewReader(""), "claude-code")
	if got != "" {
		t.Errorf("expected empty string for empty logs, got %q", got)
	}
}

func TestExtractLatestAssistantText_NonJSON(t *testing.T) {
	logs := "not json at all\nanother line\n"

	got := ExtractLatestAssistantText(strings.NewReader(logs), "claude-code")
	if got != "" {
		t.Errorf("expected empty string for non-JSON logs, got %q", got)
	}
}

func TestExtractLatestAssistantText_Truncation(t *testing.T) {
	longText := strings.Repeat("a", 600)
	logs := `{"type":"assistant","message":{"content":[{"type":"text","text":"` + longText + `"}]}}`

	got := ExtractLatestAssistantText(strings.NewReader(logs), "claude-code")
	wantLen := maxProgressLen + len("...")
	if len([]rune(got)) != wantLen {
		t.Errorf("expected truncated length %d, got %d", wantLen, len([]rune(got)))
	}
	if !strings.HasSuffix(got, "...") {
		t.Error("expected truncated text to end with ...")
	}
}

func TestExtractLatestAssistantText_NoTruncationAtLimit(t *testing.T) {
	text := strings.Repeat("b", maxProgressLen)
	logs := `{"type":"assistant","message":{"content":[{"type":"text","text":"` + text + `"}]}}`

	got := ExtractLatestAssistantText(strings.NewReader(logs), "claude-code")
	if got != text {
		t.Errorf("text at exactly maxProgressLen should not be truncated")
	}
}

// TestExtractLatestAssistantText_LargeLine verifies a single NDJSON line
// larger than 1 MB does not cause the scanner to stop consuming the stream.
// Without a sufficient scanner buffer, the parser would return an empty
// string and the subsequent assistant turn would be lost.
func TestExtractLatestAssistantText_LargeLine(t *testing.T) {
	largeText := strings.Repeat("a", 2*1024*1024)
	bigLine := `{"type":"assistant","message":{"content":[{"type":"text","text":"` + largeText + `"}]}}`
	final := `{"type":"assistant","message":{"content":[{"type":"text","text":"after the big line"}]}}`
	logs := bigLine + "\n" + final

	got := ExtractLatestAssistantText(strings.NewReader(logs), "claude-code")
	if got != "after the big line" {
		t.Errorf("expected text from the line after the >1MB line; got %q", got)
	}
}

// TestExtractLatestAssistantText_GrokDegradesGracefully documents that grok
// is a recognized agent type routed through the generic assistant-text
// extractor. grok's `--output-format json` emits a single object
// ({"text":...,"usage":...}) that does NOT match the claude-code
// assistant-message NDJSON shape, so progress text degrades to "" rather
// than crashing. grok's native progress shape is UNCONFIRMED; revisit if a
// grok-specific parser is added.
func TestExtractLatestAssistantText_GrokDegradesGracefully(t *testing.T) {
	logs := `{"text":"Fixed the bug.","stopReason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}`
	if got := ExtractLatestAssistantText(strings.NewReader(logs), "grok"); got != "" {
		t.Errorf("expected empty (grok json object is not claude-shaped assistant text), got %q", got)
	}
	if got := ExtractLatestAssistantText(strings.NewReader(""), "grok"); got != "" {
		t.Errorf("expected empty for empty grok stream, got %q", got)
	}
}

func TestExtractLatestAssistantText_UnknownAgent(t *testing.T) {
	logs := `{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}`

	got := ExtractLatestAssistantText(strings.NewReader(logs), "unknown-agent")
	if got != "" {
		t.Errorf("expected empty string for unknown agent, got %q", got)
	}
}

func TestExtractLatestAssistantText_EmptyAgentType(t *testing.T) {
	logs := `{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}`

	got := ExtractLatestAssistantText(strings.NewReader(logs), "")
	if got != "hello" {
		t.Errorf("empty agent type should default to claude-code, got %q", got)
	}
}

func TestExtractLatestAssistantText_Codex(t *testing.T) {
	logs := strings.Join([]string{
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"Checking the release tags..."}}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"Found the merge commit."}}`,
	}, "\n")

	got := ExtractLatestAssistantText(strings.NewReader(logs), "codex")
	want := "Found the merge commit."
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExtractLatestAssistantText_CodexIgnoresNonAgentMessage(t *testing.T) {
	logs := `{"type":"item.completed","item":{"type":"command_execution","text":"ls -la"}}`

	got := ExtractLatestAssistantText(strings.NewReader(logs), "codex")
	if got != "" {
		t.Errorf("expected empty string for non-agent-message item, got %q", got)
	}
}

func TestExtractLatestAssistantText_Gemini(t *testing.T) {
	logs := strings.Join([]string{
		`{"type":"init","model":"gemini-2.5-pro"}`,
		`{"type":"message","role":"assistant","content":"Searching for config files...","delta":false}`,
		`{"type":"message","role":"assistant","content":"Found the issue.","delta":false}`,
	}, "\n")

	got := ExtractLatestAssistantText(strings.NewReader(logs), "gemini")
	want := "Found the issue."
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExtractLatestAssistantText_GeminiIgnoresDelta(t *testing.T) {
	logs := `{"type":"message","role":"assistant","content":"partial...","delta":true}`

	got := ExtractLatestAssistantText(strings.NewReader(logs), "gemini")
	if got != "" {
		t.Errorf("expected empty string for delta message, got %q", got)
	}
}

func TestExtractLatestAssistantText_OpenCode(t *testing.T) {
	logs := strings.Join([]string{
		`{"type":"text","text":"Analyzing the codebase..."}`,
		`{"type":"step_finish"}`,
		`{"type":"text","text":"Identified the root cause."}`,
	}, "\n")

	got := ExtractLatestAssistantText(strings.NewReader(logs), "opencode")
	want := "Identified the root cause."
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExtractLatestAssistantText_WhitespaceOnly(t *testing.T) {
	logs := `{"type":"assistant","message":{"content":[{"type":"text","text":"   \n  "}]}}`

	got := ExtractLatestAssistantText(strings.NewReader(logs), "claude-code")
	if got != "" {
		t.Errorf("expected empty string for whitespace-only text, got %q", got)
	}
}

func TestExtractLatestAssistantText_CursorFallsBackToClaude(t *testing.T) {
	logs := `{"type":"assistant","message":{"content":[{"type":"text","text":"hello from cursor"}]}}`

	got := ExtractLatestAssistantText(strings.NewReader(logs), "cursor")
	if got != "hello from cursor" {
		t.Errorf("cursor should use claude parser, got %q", got)
	}
}
