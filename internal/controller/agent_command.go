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
