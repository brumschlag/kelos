package sessionruntime

import (
	"strings"
	"testing"
)

func TestParseSessionCommand(t *testing.T) {
	tests := []struct {
		name          string
		input         string
		kind          sessionCommandKind
		text          string
		goalAction    goalCommandAction
		goalObjective string
	}{
		{name: "message", input: "review this", kind: sessionCommandMessage, text: "review this"},
		{name: "unrecognized slash command", input: "/review", kind: sessionCommandMessage, text: "/review"},
		{name: "shell", input: "! make test", kind: sessionCommandShell, text: "make test"},
		{name: "show goal", input: "/goal", kind: sessionCommandGoal, goalAction: goalCommandShow},
		{name: "create goal", input: "/goal improve coverage", kind: sessionCommandGoal, goalAction: goalCommandCreate, goalObjective: "improve coverage"},
		{name: "multiline goal", input: "/goal\nimprove coverage", kind: sessionCommandGoal, goalAction: goalCommandCreate, goalObjective: "improve coverage"},
		{name: "edit goal", input: "/goal edit improve tests", kind: sessionCommandGoal, goalAction: goalCommandEdit, goalObjective: "improve tests"},
		{name: "clear goal", input: "/goal clear", kind: sessionCommandGoal, goalAction: goalCommandClear},
		{name: "pause goal", input: "/goal pause", kind: sessionCommandGoal, goalAction: goalCommandPause},
		{name: "resume goal", input: "/goal resume", kind: sessionCommandGoal, goalAction: goalCommandResume},
		{name: "objective starts with command", input: "/goal clear tech debt", kind: sessionCommandGoal, goalAction: goalCommandCreate, goalObjective: "clear tech debt"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command, err := parseSessionCommand(test.input)
			if err != nil {
				t.Fatal(err)
			}
			if command.kind != test.kind || command.text != test.text || command.goal.action != test.goalAction || command.goal.objective != test.goalObjective {
				t.Fatalf("parseSessionCommand(%q) = %#v", test.input, command)
			}
		})
	}
}

func TestParseSessionCommandRejectsInvalidCommands(t *testing.T) {
	for _, input := range []string{"!", "/goal edit"} {
		t.Run(strings.ReplaceAll(input, " ", "_"), func(t *testing.T) {
			if _, err := parseSessionCommand(input); err == nil {
				t.Fatalf("parseSessionCommand(%q) returned no error", input)
			}
		})
	}
}
