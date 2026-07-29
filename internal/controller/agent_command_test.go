package controller

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestAgentProcessCommand(t *testing.T) {
	tests := []struct {
		name    string
		useTini bool
		want    []string
	}{
		{
			name:    "bundled image",
			useTini: true,
			want:    []string{tiniPath, "-g", "--", "/kelos_entrypoint.sh"},
		},
		{
			name:    "custom image",
			useTini: false,
			want:    []string{"/kelos_entrypoint.sh"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := agentProcessCommand("/kelos_entrypoint.sh", tt.useTini)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("agentProcessCommand() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsBundledAgentImage(t *testing.T) {
	tests := []struct {
		image string
		want  bool
	}{
		{image: ClaudeCodeImageRepository, want: true},
		{image: CodexImage, want: true},
		{image: GeminiImageRepository + "@sha256:abc", want: true},
		{image: "mirror.example/kelos-dev/opencode:latest", want: false},
		{image: CursorImageRepository + "-custom:latest", want: false},
		{image: "custom-agent:latest", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.image, func(t *testing.T) {
			if got := isBundledAgentImage(tt.image); got != tt.want {
				t.Fatalf("isBundledAgentImage(%q) = %t, want %t", tt.image, got, tt.want)
			}
		})
	}
}

func TestBundledAgentImagesUseTini(t *testing.T) {
	tests := []struct {
		dir        string
		entrypoint string
	}{
		{dir: "claude-code", entrypoint: "claude"},
		{dir: "codex", entrypoint: "codex"},
		{dir: "gemini", entrypoint: "gemini"},
		{dir: "opencode", entrypoint: "opencode"},
		{dir: "cursor", entrypoint: "agent"},
	}

	for _, tt := range tests {
		t.Run(tt.dir, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("..", "..", tt.dir, "Dockerfile"))
			if err != nil {
				t.Fatalf("reading Dockerfile: %v", err)
			}
			content := string(data)
			if !strings.Contains(content, "\n    tini \\\n") {
				t.Fatal("Dockerfile does not install tini")
			}
			wantEntrypoint := `ENTRYPOINT ["/usr/bin/tini", "-g", "--", "` + tt.entrypoint + `"]`
			if !strings.Contains(content, wantEntrypoint) {
				t.Fatalf("Dockerfile does not contain %q", wantEntrypoint)
			}
		})
	}
}

func assertAgentProcessCommand(t *testing.T, got []string, program string, useTini bool) {
	t.Helper()
	want := agentProcessCommand(program, useTini)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("agent command = %v, want %v", got, want)
	}
}
