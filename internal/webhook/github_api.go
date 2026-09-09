package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/go-logr/logr"
)

// githubHTTPClient is used for all GitHub API requests, with a reasonable
// timeout to avoid blocking webhook processing if the API is unresponsive.
var githubHTTPClient = &http.Client{Timeout: 10 * time.Second}

// githubPRHeadInfo contains head branch and SHA returned from the GitHub API.
type githubPRHeadInfo struct {
	Branch string
	SHA    string
}

// githubPRBranchFetcher fetches a PR's head info from the GitHub API given an
// explicit token. It is a package-level variable so tests can swap in a stub.
var githubPRBranchFetcher = fetchGitHubPRBranchWithToken

// resolveGitHubToken returns the token configured for this handler.
func (h *WebhookHandler) resolveGitHubToken(ctx context.Context) (string, error) {
	if h.githubTokenResolver == nil {
		return "", nil
	}
	return h.githubTokenResolver(ctx)
}

// githubPRResponse is the minimal structure needed to extract the head branch
// and SHA from a GitHub pull request API response.
type githubPRResponse struct {
	Head struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
}

// fetchGitHubPRBranchWithToken fetches the head branch and SHA for a pull
// request using the GitHub REST API. The prAPIURL comes from the webhook
// payload, so it already targets the correct host (github.com or a GitHub
// Enterprise instance). Returns a zero-value githubPRHeadInfo when the token is
// empty, allowing callers to fall back gracefully.
func fetchGitHubPRBranchWithToken(ctx context.Context, prAPIURL, token string) (githubPRHeadInfo, error) {
	if token == "" {
		return githubPRHeadInfo{}, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, prAPIURL, nil)
	if err != nil {
		return githubPRHeadInfo{}, fmt.Errorf("creating GitHub API request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := githubHTTPClient.Do(req)
	if err != nil {
		return githubPRHeadInfo{}, fmt.Errorf("calling GitHub API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return githubPRHeadInfo{}, fmt.Errorf("GitHub API returned status %d: %s", resp.StatusCode, string(body))
	}

	var pr githubPRResponse
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return githubPRHeadInfo{}, fmt.Errorf("decoding GitHub API response: %w", err)
	}

	return githubPRHeadInfo{Branch: pr.Head.Ref, SHA: pr.Head.SHA}, nil
}

// enrichGitHubIssueCommentBranch fetches the PR's head branch and SHA from the
// GitHub API and sets them on the event data. This is called lazily for
// issue_comment events on pull requests, since GitHub does not include the PR's
// head ref or SHA in the issue_comment webhook payload.
func (h *WebhookHandler) enrichGitHubIssueCommentBranch(ctx context.Context, log logr.Logger, eventData *GitHubEventData) {
	if eventData.PullRequestAPIURL == "" {
		return
	}

	token, err := h.resolveGitHubToken(ctx)
	if err != nil {
		log.Error(err, "Failed to resolve GitHub token for issue_comment enrichment", "prAPIURL", eventData.PullRequestAPIURL)
		return
	}

	head, err := githubPRBranchFetcher(ctx, eventData.PullRequestAPIURL, token)
	if err != nil {
		log.Error(err, "Failed to fetch PR head for issue_comment event", "prAPIURL", eventData.PullRequestAPIURL)
		return
	}
	if head.Branch == "" {
		log.Info("No GitHub credentials configured, cannot enrich issue_comment event with PR head")
		return
	}

	log.Info("Enriched issue_comment event with PR head", "branch", head.Branch, "sha", head.SHA)
	eventData.Branch = head.Branch
	eventData.HeadSHA = head.SHA
}
