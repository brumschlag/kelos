package controller

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// preCommands/postCommands are honoured by the pooled worker-runner
// (internal/workerrunner) but the non-pooled Job launches /kelos_entrypoint.sh
// directly, so the fields were accepted, stored, and silently ignored. A Task
// requesting them reported Succeeded having never run them — a phase that
// believed it was guarded ran unguarded.
//
// Wrapping in the controller rather than each agent image keeps custom and
// shell-less images working and leaves one place to change.
func TestAgentProcessCommandWithHooks(t *testing.T) {
	pre := [][]string{{"sh", "-c", "echo before"}}
	post := [][]string{{"sh", "-c", "echo after"}}

	t.Run("no hooks runs the entrypoint directly", func(t *testing.T) {
		got := agentProcessCommandWithHooks("/kelos_entrypoint.sh", true, nil, nil)
		want := []string{tiniPath, "-g", "--", "/kelos_entrypoint.sh"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("hooks wrap the entrypoint in a shell", func(t *testing.T) {
		got := agentProcessCommandWithHooks("/kelos_entrypoint.sh", true, pre, post)

		// Still launched under Tini, so signal handling is unchanged.
		if got[0] != tiniPath {
			t.Fatalf("expected tini first, got %v", got[0])
		}
		script := strings.Join(got, " ")
		if !strings.Contains(script, "echo before") {
			t.Errorf("preCommand missing from %q", script)
		}
		if !strings.Contains(script, "echo after") {
			t.Errorf("postCommand missing from %q", script)
		}
		if !strings.Contains(script, "/kelos_entrypoint.sh") {
			t.Errorf("entrypoint missing from %q", script)
		}
	})

	t.Run("a failing preCommand must not start the agent", func(t *testing.T) {
		// The pooled runner returns before running the agent, because a missing
		// baseline yields a result attributed to the wrong Task. Same contract here.
		got := agentProcessCommandWithHooks("/kelos_entrypoint.sh", false, pre, nil)
		script := strings.Join(got, " ")
		if !strings.Contains(script, "set -e") {
			t.Errorf("expected set -e so a failed preCommand aborts, got %q", script)
		}
		if strings.Index(script, "echo before") > strings.Index(script, "/kelos_entrypoint.sh") {
			t.Errorf("preCommand must precede the entrypoint: %q", script)
		}
	})

	t.Run("postCommands run after the agent and preserve its exit code", func(t *testing.T) {
		// A postCommand is a transfer step; losing the agent's exit status would
		// make a failed agent look successful.
		got := agentProcessCommandWithHooks("/kelos_entrypoint.sh", false, nil, post)
		script := strings.Join(got, " ")
		if strings.Index(script, "echo after") < strings.Index(script, "/kelos_entrypoint.sh") {
			t.Errorf("postCommand must follow the entrypoint: %q", script)
		}
		if !strings.Contains(script, "exit") {
			t.Errorf("expected the agent's exit code to be propagated: %q", script)
		}
	})

	// Asserted by EXECUTION, not by inspecting the string: a hand-written
	// "is it quoted" check is easy to get wrong (the first version of this test
	// was), whereas running it proves the outer shell treats the payload as data.
	t.Run("metacharacters in a command are data, not host-shell syntax", func(t *testing.T) {
		marker := filepath.Join(t.TempDir(), "breakout")
		// If quoting fails, the outer shell expands this and creates the file.
		nasty := [][]string{{"sh", "-c", "echo skipped > /dev/null"}, {"true", "$(touch " + marker + ")"}}

		got := agentProcessCommandWithHooks("/bin/true", false, nasty, nil)
		if err := exec.Command(got[0], got[1:]...).Run(); err != nil {
			t.Fatalf("wrapped command failed: %v", err)
		}
		if _, err := os.Stat(marker); err == nil {
			t.Errorf("command substitution escaped quoting and ran on the host shell")
		}
	})

	// Foreman's hook install writes its script with a heredoc, so newlines and
	// nested quotes have to survive intact.
	t.Run("a multi-line command with nested quotes survives", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "written")
		heredoc := "cat > " + out + " <<'INNER'\n#!/bin/sh\necho \"it's fine\"\nINNER"
		got := agentProcessCommandWithHooks("/bin/true", false, [][]string{{"sh", "-c", heredoc}}, nil)

		if err := exec.Command(got[0], got[1:]...).Run(); err != nil {
			t.Fatalf("wrapped command failed: %v", err)
		}
		data, err := os.ReadFile(out)
		if err != nil {
			t.Fatalf("preCommand did not run: %v", err)
		}
		if !strings.Contains(string(data), "it's fine") {
			t.Errorf("heredoc content mangled: %q", string(data))
		}
	})
}
