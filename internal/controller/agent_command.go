package controller

import "strings"

const tiniPath = "/usr/bin/tini"

var bundledAgentImageRepositories = []string{
	ClaudeCodeImageRepository,
	CodexImageRepository,
	GeminiImageRepository,
	OpenCodeImageRepository,
	CursorImageRepository,
}

func isBundledAgentImage(image string) bool {
	for _, repository := range bundledAgentImageRepositories {
		if image == repository ||
			strings.HasPrefix(image, repository+":") ||
			strings.HasPrefix(image, repository+"@") {
			return true
		}
	}
	return false
}

func agentProcessCommand(program string, useTini bool) []string {
	if !useTini {
		return []string{program}
	}
	return []string{tiniPath, "-g", "--", program}
}

// agentProcessCommandWithHooks runs a Task's pre/postCommands around the agent
// entrypoint.
//
// The pooled path executes these in Go (internal/workerrunner), but a non-pooled
// Job launches the entrypoint directly, so without this the fields are accepted,
// stored, and silently ignored — a Task requesting them reports Succeeded having
// never run them.
//
// Wrapping here rather than in each agent image keeps custom and shell-less
// images working: only a Task that actually uses hooks gains a shell dependency,
// and there is one place to change instead of five Dockerfiles.
//
// Semantics match the pooled runner: `set -e` so a failed preCommand aborts
// before the agent starts (a missing baseline would attribute a result to the
// wrong Task), and the agent's exit status survives postCommands (a transfer step
// must not make a failed agent look successful).
func agentProcessCommandWithHooks(program string, useTini bool, pre, post [][]string) []string {
	if len(pre) == 0 && len(post) == 0 {
		return agentProcessCommand(program, useTini)
	}

	var script strings.Builder
	script.WriteString("set -e\n")
	for _, command := range pre {
		script.WriteString(shellQuoteArgs(command))
		script.WriteString("\n")
	}
	// The agent's own failure must not abort before postCommands run, so drop out
	// of -e for it and re-raise its status at the end.
	script.WriteString("set +e\n")
	script.WriteString(shellQuoteArgs([]string{program}))
	script.WriteString(" \"$@\"\n")
	script.WriteString("__kelos_agent_status=$?\nset -e\n")
	for _, command := range post {
		script.WriteString(shellQuoteArgs(command))
		script.WriteString("\n")
	}
	script.WriteString("exit \"$__kelos_agent_status\"\n")

	// Trailing "sh" sets $0 so the prompt in Args lands in "$@" instead of being
	// consumed as the shell's own name.
	shell := []string{"/bin/sh", "-c", script.String(), "sh"}
	if !useTini {
		return shell
	}
	return append([]string{tiniPath, "-g", "--"}, shell...)
}

// shellQuoteArgs renders an exec-form command as shell words, single-quoting every
// argument so metacharacters, newlines, and substitutions inside a Task's commands
// are data rather than host-shell syntax.
func shellQuoteArgs(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, "'"+strings.ReplaceAll(arg, "'", `'\''`)+"'")
	}
	return strings.Join(quoted, " ")
}
