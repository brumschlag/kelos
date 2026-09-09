package webhook

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-logr/logr"
)

func TestFetchGitHubPRBranchWithToken(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		response   interface{}
		wantBranch string
		wantSHA    string
		wantErr    bool
	}{
		{
			name:       "successful fetch",
			statusCode: http.StatusOK,
			response: map[string]interface{}{
				"head": map[string]interface{}{
					"ref": "feature-branch",
					"sha": "abc123",
				},
			},
			wantBranch: "feature-branch",
			wantSHA:    "abc123",
		},
		{
			name:       "API error",
			statusCode: http.StatusNotFound,
			response:   map[string]string{"message": "Not Found"},
			wantErr:    true,
		},
		{
			name:       "empty head ref",
			statusCode: http.StatusOK,
			response: map[string]interface{}{
				"head": map[string]interface{}{
					"ref": "",
					"sha": "",
				},
			},
			wantBranch: "",
			wantSHA:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer test-token" {
					t.Errorf("Expected Authorization header 'Bearer test-token', got %q", r.Header.Get("Authorization"))
				}
				if r.Header.Get("Accept") != "application/vnd.github+json" {
					t.Errorf("Expected Accept header 'application/vnd.github+json', got %q", r.Header.Get("Accept"))
				}
				w.WriteHeader(tt.statusCode)
				json.NewEncoder(w).Encode(tt.response)
			}))
			defer server.Close()

			head, err := fetchGitHubPRBranchWithToken(context.Background(), server.URL, "test-token")
			if (err != nil) != tt.wantErr {
				t.Errorf("fetchGitHubPRBranchWithToken() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if head.Branch != tt.wantBranch {
				t.Errorf("fetchGitHubPRBranchWithToken() Branch = %q, want %q", head.Branch, tt.wantBranch)
			}
			if head.SHA != tt.wantSHA {
				t.Errorf("fetchGitHubPRBranchWithToken() SHA = %q, want %q", head.SHA, tt.wantSHA)
			}
		})
	}
}

func TestFetchGitHubPRBranchWithToken_EmptyToken(t *testing.T) {
	head, err := fetchGitHubPRBranchWithToken(context.Background(), "http://unused", "")
	if err != nil {
		t.Errorf("Expected no error for empty token, got %v", err)
	}
	if head.Branch != "" {
		t.Errorf("Expected empty branch for empty token, got %q", head.Branch)
	}
}

func TestResolveGitHubToken(t *testing.T) {
	t.Run("nil resolver returns empty token", func(t *testing.T) {
		h := &WebhookHandler{}
		token, err := h.resolveGitHubToken(context.Background())
		if err != nil {
			t.Fatalf("resolveGitHubToken() error = %v", err)
		}
		if token != "" {
			t.Errorf("resolveGitHubToken() = %q, want empty", token)
		}
	})

	t.Run("uses configured resolver", func(t *testing.T) {
		h := &WebhookHandler{githubTokenResolver: func(context.Context) (string, error) { return "configured", nil }}
		token, err := h.resolveGitHubToken(context.Background())
		if err != nil {
			t.Fatalf("resolveGitHubToken() error = %v", err)
		}
		if token != "configured" {
			t.Errorf("resolveGitHubToken() = %q, want %q", token, "configured")
		}
	})
}

func TestEnrichGitHubIssueCommentBranch(t *testing.T) {
	// Swap in a stub fetcher
	orig := githubPRBranchFetcher
	defer func() { githubPRBranchFetcher = orig }()

	t.Run("enriches branch and SHA from API", func(t *testing.T) {
		githubPRBranchFetcher = func(ctx context.Context, prAPIURL, token string) (githubPRHeadInfo, error) {
			if prAPIURL != "https://api.github.com/repos/org/repo/pulls/42" {
				t.Errorf("Unexpected prAPIURL: %s", prAPIURL)
			}
			if token != "per-gateway-token" {
				t.Errorf("Expected per-gateway token, got %q", token)
			}
			return githubPRHeadInfo{Branch: "my-feature-branch", SHA: "abc123sha"}, nil
		}

		h := &WebhookHandler{githubTokenResolver: func(context.Context) (string, error) { return "per-gateway-token", nil }}
		eventData := &GitHubEventData{
			PullRequestAPIURL: "https://api.github.com/repos/org/repo/pulls/42",
		}

		h.enrichGitHubIssueCommentBranch(context.Background(), logr.Discard(), eventData)

		if eventData.Branch != "my-feature-branch" {
			t.Errorf("Expected Branch = %q, got %q", "my-feature-branch", eventData.Branch)
		}
		if eventData.HeadSHA != "abc123sha" {
			t.Errorf("Expected HeadSHA = %q, got %q", "abc123sha", eventData.HeadSHA)
		}
	})

	t.Run("no-op when PullRequestAPIURL is empty", func(t *testing.T) {
		githubPRBranchFetcher = func(ctx context.Context, prAPIURL, token string) (githubPRHeadInfo, error) {
			t.Error("Fetcher should not be called when PullRequestAPIURL is empty")
			return githubPRHeadInfo{}, nil
		}

		h := &WebhookHandler{}
		eventData := &GitHubEventData{}
		h.enrichGitHubIssueCommentBranch(context.Background(), logr.Discard(), eventData)

		if eventData.Branch != "" {
			t.Errorf("Expected empty Branch, got %q", eventData.Branch)
		}
		if eventData.HeadSHA != "" {
			t.Errorf("Expected empty HeadSHA, got %q", eventData.HeadSHA)
		}
	})

	t.Run("handles no credentials gracefully", func(t *testing.T) {
		githubPRBranchFetcher = func(ctx context.Context, prAPIURL, token string) (githubPRHeadInfo, error) {
			return githubPRHeadInfo{}, nil // simulates no credentials configured
		}

		h := &WebhookHandler{}
		eventData := &GitHubEventData{
			PullRequestAPIURL: "https://api.github.com/repos/org/repo/pulls/42",
		}

		h.enrichGitHubIssueCommentBranch(context.Background(), logr.Discard(), eventData)

		if eventData.Branch != "" {
			t.Errorf("Expected empty Branch when no credentials configured, got %q", eventData.Branch)
		}
		if eventData.HeadSHA != "" {
			t.Errorf("Expected empty HeadSHA when no credentials configured, got %q", eventData.HeadSHA)
		}
	})
}
