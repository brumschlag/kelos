package reporting

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
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

type labelResponse struct {
	Name string `json:"name"`
}

type addLabelsRequest struct {
	Labels []string `json:"labels"`
}

// RemoveLabel removes a label from a GitHub issue or pull request.
//
// A 404 is reported as success. GitHub answers 404 when the label is not on the
// issue, which is the desired end state, so the call is idempotent and safe to
// repeat — and repeating is normal here, because the reporter can re-run for the
// same phase after a controller restart or a failed annotation persist. Any
// other non-2xx status IS returned as an error: folding a 403 into success would
// hide a missing Issues:write scope, i.e. hide exactly the leak this method
// exists to close.
func (r *GitHubReporter) RemoveLabel(ctx context.Context, number int, label string) error {
	url := fmt.Sprintf("%s/repos/%s/%s/issues/%d/labels/%s",
		r.baseURL(), r.Owner, r.Repo, number, neturl.PathEscape(label))

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	r.setHeaders(req)

	resp, err := r.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("removing label %q from #%d: %w", label, number, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("GitHub API returned status %d removing label %q from #%d: %s",
			resp.StatusCode, label, number, string(errBody))
	}

	return nil
}

// AddLabels adds labels to a GitHub issue or pull request. Adding a label that
// is already present is a no-op on GitHub's side, so this is idempotent too.
// An empty list spends no API call.
func (r *GitHubReporter) AddLabels(ctx context.Context, number int, labels []string) error {
	if len(labels) == 0 {
		return nil
	}

	url := fmt.Sprintf("%s/repos/%s/%s/issues/%d/labels", r.baseURL(), r.Owner, r.Repo, number)

	payload, err := json.Marshal(addLabelsRequest{Labels: labels})
	if err != nil {
		return fmt.Errorf("marshalling labels: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	r.setHeaders(req)

	resp, err := r.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("adding labels %v to #%d: %w", labels, number, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("GitHub API returned status %d adding labels %v to #%d: %s",
			resp.StatusCode, labels, number, string(errBody))
	}

	return nil
}

// ListLabels returns the names of the labels currently on a GitHub issue or
// pull request.
func (r *GitHubReporter) ListLabels(ctx context.Context, number int) ([]string, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/issues/%d/labels?per_page=100", r.baseURL(), r.Owner, r.Repo, number)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	r.setHeaders(req)

	resp, err := r.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("listing labels on #%d: %w", number, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("GitHub API returned status %d listing labels on #%d: %s",
			resp.StatusCode, number, string(errBody))
	}

	var payload []labelResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decoding labels response: %w", err)
	}

	names := make([]string, 0, len(payload))
	for _, l := range payload {
		names = append(names, l.Name)
	}
	return names, nil
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

// FormatFailedComment returns the comment body for a failed task.
func FormatFailedComment(taskName string) string {
	return fmt.Sprintf("🤖 **Kelos Task Status**\n\nTask `%s` has **failed**. ❌", taskName)
}
