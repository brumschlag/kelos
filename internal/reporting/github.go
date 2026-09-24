package reporting

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

const defaultBaseURL = "https://api.github.com"

// GitHubReporter posts and updates issue/PR comments on GitHub.
// TokenFunc, when set, is called on every API request to resolve the current
// token. This supports dynamic credentials such as GitHub App installation
// tokens that are refreshed in-process. When TokenFunc is nil the static
// Token field is used instead.
type GitHubReporter struct {
	Owner       string
	Repo        string
	Token       string        // static token (used when TokenFunc is nil)
	TokenFunc   func() string // dynamic token resolver; takes precedence over Token
	GitHubAppID string        // app ID when TokenFunc returns an installation token
	BaseURL     string
	Client      *http.Client
}

func (r *GitHubReporter) baseURL() string {
	if r.BaseURL != "" {
		return r.BaseURL
	}
	return defaultBaseURL
}

func (r *GitHubReporter) httpClient() *http.Client {
	if r.Client != nil {
		return r.Client
	}
	return http.DefaultClient
}

type createCommentRequest struct {
	Body string `json:"body"`
}

type commentResponse struct {
	ID                    int64      `json:"id"`
	Body                  string     `json:"body"`
	User                  githubUser `json:"user"`
	PerformedViaGitHubApp *githubApp `json:"performed_via_github_app"`
}

type githubUser struct {
	Login string `json:"login"`
}

type githubApp struct {
	ID int64 `json:"id"`
}

type commentOwner struct {
	userLogin   string
	githubAppID int64
}

func (o commentOwner) owns(comment commentResponse) bool {
	if o.githubAppID != 0 {
		return comment.PerformedViaGitHubApp != nil && comment.PerformedViaGitHubApp.ID == o.githubAppID
	}
	return strings.EqualFold(comment.User.Login, o.userLogin)
}

// FindCommentByMarker returns the newest comment containing marker, or zero
// when no matching comment exists.
func (r *GitHubReporter) FindCommentByMarker(ctx context.Context, number int, marker string) (int64, error) {
	owner, err := r.commentOwner(ctx)
	if err != nil {
		return 0, err
	}

	var foundID int64
	for page := 1; ; page++ {
		url := fmt.Sprintf("%s/repos/%s/%s/issues/%d/comments?per_page=100&page=%d", r.baseURL(), r.Owner, r.Repo, number, page)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return 0, fmt.Errorf("creating request: %w", err)
		}
		r.setHeaders(req)

		resp, err := r.httpClient().Do(req)
		if err != nil {
			return 0, fmt.Errorf("listing comments: %w", err)
		}
		if resp.StatusCode != http.StatusOK {
			errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			resp.Body.Close()
			return 0, fmt.Errorf("GitHub API returned status %d: %s", resp.StatusCode, string(errBody))
		}

		var comments []commentResponse
		if err := json.NewDecoder(resp.Body).Decode(&comments); err != nil {
			resp.Body.Close()
			return 0, fmt.Errorf("decoding comments response: %w", err)
		}
		resp.Body.Close()

		for _, comment := range comments {
			if strings.Contains(comment.Body, marker) && owner.owns(comment) {
				foundID = comment.ID
			}
		}
		if len(comments) < 100 {
			return foundID, nil
		}
	}
}

func (r *GitHubReporter) commentOwner(ctx context.Context) (commentOwner, error) {
	if r.GitHubAppID != "" {
		appID, err := strconv.ParseInt(r.GitHubAppID, 10, 64)
		if err != nil || appID <= 0 {
			return commentOwner{}, fmt.Errorf("invalid GitHub App ID %q", r.GitHubAppID)
		}
		return commentOwner{githubAppID: appID}, nil
	}

	url := r.baseURL() + "/user"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return commentOwner{}, fmt.Errorf("creating request: %w", err)
	}
	r.setHeaders(req)

	resp, err := r.httpClient().Do(req)
	if err != nil {
		return commentOwner{}, fmt.Errorf("getting authenticated GitHub user: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return commentOwner{}, fmt.Errorf("GitHub API returned status %d: %s", resp.StatusCode, string(errBody))
	}

	var user githubUser
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return commentOwner{}, fmt.Errorf("decoding authenticated user response: %w", err)
	}
	if user.Login == "" {
		return commentOwner{}, fmt.Errorf("authenticated GitHub user response has no login")
	}
	return commentOwner{userLogin: user.Login}, nil
}

// CreateComment creates a comment on a GitHub issue or pull request and returns
// the comment ID.
func (r *GitHubReporter) CreateComment(ctx context.Context, number int, body string) (int64, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/issues/%d/comments", r.baseURL(), r.Owner, r.Repo, number)

	payload, err := json.Marshal(createCommentRequest{Body: body})
	if err != nil {
		return 0, fmt.Errorf("marshalling comment body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return 0, fmt.Errorf("creating request: %w", err)
	}
	r.setHeaders(req)

	resp, err := r.httpClient().Do(req)
	if err != nil {
		return 0, fmt.Errorf("posting comment: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return 0, fmt.Errorf("GitHub API returned status %d: %s", resp.StatusCode, string(errBody))
	}

	var result commentResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, fmt.Errorf("decoding comment response: %w", err)
	}

	return result.ID, nil
}

// UpdateComment updates an existing GitHub comment by its ID.
func (r *GitHubReporter) UpdateComment(ctx context.Context, commentID int64, body string) error {
	url := fmt.Sprintf("%s/repos/%s/%s/issues/comments/%s", r.baseURL(), r.Owner, r.Repo, strconv.FormatInt(commentID, 10))

	payload, err := json.Marshal(createCommentRequest{Body: body})
	if err != nil {
		return fmt.Errorf("marshalling comment body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	r.setHeaders(req)

	resp, err := r.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("updating comment: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("GitHub API returned status %d: %s", resp.StatusCode, string(errBody))
	}

	return nil
}

// resolveToken returns the current GitHub token. When TokenFunc is set it
// is called to resolve the token dynamically. Falls back to the static
// Token field.
func (r *GitHubReporter) resolveToken() string {
	if r.TokenFunc != nil {
		return r.TokenFunc()
	}
	return r.Token
}

func (r *GitHubReporter) setHeaders(req *http.Request) {
	if token := r.resolveToken(); token != "" {
		req.Header.Set("Authorization", "token "+token)
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("Content-Type", "application/json")
}

// FormatAcceptedComment returns the comment body for an accepted task.
func FormatAcceptedComment(taskName string) string {
	return fmt.Sprintf("🤖 **Kelos Task Status**\n\nTask `%s` has been **accepted** and is being processed.", taskName)
}

// FormatSucceededComment returns the comment body for a succeeded task.
func FormatSucceededComment(taskName string) string {
	return fmt.Sprintf("🤖 **Kelos Task Status**\n\nTask `%s` has **succeeded**. ✅", taskName)
}

// maxFailureCauseChars caps how much of the agent's final message a failed
// status comment quotes.
const maxFailureCauseChars = 1500

// secretPatterns match common credential formats. The agent's final message
// is agent-authored text and may echo credentials it saw, so it is redacted
// before being posted.
// Only sk- keys require a word boundary, so text like "task-management-..."
// is not redacted; the other prefixes are distinctive enough to match even
// when glued to surrounding text.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}`),
	regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`),
	regexp.MustCompile(`(?:AKIA|ASIA)[0-9A-Z]{16}`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}`),
	regexp.MustCompile(`xox[abposr]-[A-Za-z0-9-]{10,}`),
}

func redactSecrets(s string) string {
	for _, re := range secretPatterns {
		s = re.ReplaceAllString(s, "[REDACTED]")
	}
	return s
}

// FormatFailedComment returns the comment body for a failed task. It quotes
// the agent's final message (the base64 "response" result) as the cause,
// redacted and truncated, plus the cost when reported. When no response was
// captured it says so and includes the controller's status message instead.
func FormatFailedComment(taskName, statusMessage string, results map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "🤖 **Kelos Task Status**\n\nTask `%s` has **failed**. ❌", taskName)

	if response := strings.TrimSpace(decodeResponse(results["response"])); response != "" {
		response = redactSecrets(response)
		runes := []rune(response)
		truncated := len(runes) > maxFailureCauseChars
		if truncated {
			response = string(runes[:maxFailureCauseChars])
		}
		fence := codeFence(response)
		fmt.Fprintf(&b, "\n\n**Cause** (final agent message):\n\n%stext\n%s\n%s", fence, response, fence)
		if truncated {
			fmt.Fprintf(&b, "\n\n_Truncated: showing the first %d of %d characters._", maxFailureCauseChars, len(runes))
		}
	} else {
		b.WriteString("\n\nNo agent response was captured for this run.")
		if msg := strings.TrimSpace(statusMessage); msg != "" {
			fmt.Fprintf(&b, "\n\n**Controller status:** %s", redactSecrets(msg))
		}
	}

	if cost, err := strconv.ParseFloat(results["cost-usd"], 64); err == nil {
		fmt.Fprintf(&b, "\n\n**Cost:** $%.2f", cost)
	}
	return b.String()
}

// codeFence returns a backtick fence longer than any backtick run in s, so s
// cannot close the fence and inject markdown (mentions, links) into the
// comment.
func codeFence(s string) string {
	longest, run := 0, 0
	for _, r := range s {
		if r == '`' {
			run++
			if run > longest {
				longest = run
			}
		} else {
			run = 0
		}
	}
	return strings.Repeat("`", max(3, longest+1))
}
