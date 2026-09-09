package sessionruntime

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

const goalUsage = "usage: /goal [<objective>|clear|edit <objective>|pause|resume]"

type sessionCommandKind string

const (
	sessionCommandMessage sessionCommandKind = "message"
	sessionCommandShell   sessionCommandKind = "shell"
	sessionCommandGoal    sessionCommandKind = "goal"
)

type sessionCommand struct {
	kind sessionCommandKind
	text string
	goal goalCommand
}

type goalCommandAction string

const (
	goalCommandShow   goalCommandAction = "show"
	goalCommandCreate goalCommandAction = "create"
	goalCommandEdit   goalCommandAction = "edit"
	goalCommandClear  goalCommandAction = "clear"
	goalCommandPause  goalCommandAction = "pause"
	goalCommandResume goalCommandAction = "resume"
)

type goalCommand struct {
	action    goalCommandAction
	objective string
}

func parseSessionCommand(text string) (sessionCommand, error) {
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "!") {
		command := strings.TrimSpace(strings.TrimPrefix(trimmed, "!"))
		if command == "" {
			return sessionCommand{}, errors.New("shell command must not be empty")
		}
		return sessionCommand{kind: sessionCommandShell, text: command}, nil
	}
	if !hasCommandPrefix(trimmed, "/goal") {
		return sessionCommand{kind: sessionCommandMessage, text: text}, nil
	}

	arguments := strings.TrimSpace(strings.TrimPrefix(trimmed, "/goal"))
	if arguments == "" {
		return sessionCommand{kind: sessionCommandGoal, goal: goalCommand{action: goalCommandShow}}, nil
	}
	fields := strings.Fields(arguments)
	switch fields[0] {
	case "clear":
		if len(fields) == 1 {
			return sessionCommand{kind: sessionCommandGoal, goal: goalCommand{action: goalCommandClear}}, nil
		}
	case "pause":
		if len(fields) == 1 {
			return sessionCommand{kind: sessionCommandGoal, goal: goalCommand{action: goalCommandPause}}, nil
		}
	case "resume":
		if len(fields) == 1 {
			return sessionCommand{kind: sessionCommandGoal, goal: goalCommand{action: goalCommandResume}}, nil
		}
	case "edit":
		objective := strings.TrimSpace(strings.TrimPrefix(arguments, fields[0]))
		if objective == "" {
			return sessionCommand{}, errors.New(goalUsage)
		}
		return sessionCommand{kind: sessionCommandGoal, goal: goalCommand{action: goalCommandEdit, objective: objective}}, nil
	}
	return sessionCommand{kind: sessionCommandGoal, goal: goalCommand{action: goalCommandCreate, objective: arguments}}, nil
}

func hasCommandPrefix(text, command string) bool {
	if text == command {
		return true
	}
	if !strings.HasPrefix(text, command) || len(text) == len(command) {
		return false
	}
	value, _ := utf8.DecodeRuneInString(text[len(command):])
	return unicode.IsSpace(value)
}
