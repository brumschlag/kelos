package sessionruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

const (
	workspaceStatusFileName       = "workspace-status.json"
	workspaceStatusCommandTimeout = 5 * time.Second
	pullRequestStatusQuery        = `query($owner:String!,$repo:String!,$number:Int!){repository(owner:$owner,name:$repo){pullRequest(number:$number){url state isDraft commits(last:1){nodes{commit{statusCheckRollup{contexts(first:100){nodes{__typename ... on CheckRun{status conclusion} ... on StatusContext{state}}}}}}} mergeQueueEntry{headCommit{statusCheckRollup{contexts(first:100){nodes{__typename ... on CheckRun{status conclusion} ... on StatusContext{state}}}}}}}}}`
)

// WorkspaceStatus describes the git work currently visible in a Session.
type WorkspaceStatus struct {
	Branch      string                    `json:"branch,omitempty"`
	PullRequest *kelos.SessionPullRequest `json:"pullRequest,omitempty"`
}

type workspaceStatusRunner interface {
	run(context.Context, string, string, ...string) (string, error)
}

type githubStatusCheck struct {
	TypeName   string `json:"__typename"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	State      string `json:"state"`
}

type githubRepository struct {
	Host  string
	Owner string
	Name  string
}

type githubStatusCheckRollup struct {
	Contexts struct {
		Nodes []githubStatusCheck `json:"nodes"`
	} `json:"contexts"`
}

type githubCommit struct {
	StatusCheckRollup *githubStatusCheckRollup `json:"statusCheckRollup"`
}

type githubMergeQueueEntry struct {
	HeadCommit *githubCommit `json:"headCommit"`
}

type githubPullRequestStatus struct {
	URL     string `json:"url"`
	State   string `json:"state"`
	IsDraft bool   `json:"isDraft"`
	Commits struct {
		Nodes []struct {
			Commit githubCommit `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
	MergeQueueEntry *githubMergeQueueEntry `json:"mergeQueueEntry"`
}

type realWorkspaceStatusRunner struct{}

func (realWorkspaceStatusRunner) run(ctx context.Context, workingDir, name string, args ...string) (string, error) {
	commandCtx, cancel := context.WithTimeout(ctx, workspaceStatusCommandTimeout)
	defer cancel()
	command := exec.CommandContext(commandCtx, name, args...)
	command.Dir = workingDir
	output, err := command.Output()
	return strings.TrimSpace(string(output)), err
}

func captureWorkspaceStatus(ctx context.Context, runner workspaceStatusRunner, workingDir string, environment []string) (WorkspaceStatus, error) {
	return captureWorkspaceStatusFromPrevious(ctx, runner, workingDir, environment, WorkspaceStatus{}, true)
}

func captureWorkspaceStatusFromPrevious(ctx context.Context, runner workspaceStatusRunner, workingDir string, environment []string, previous WorkspaceStatus, forceDiscovery bool) (WorkspaceStatus, error) {
	isGitWorkspace, err := isGitWorkspace(ctx, runner, workingDir)
	if err != nil {
		return WorkspaceStatus{}, err
	}
	if !isGitWorkspace {
		return WorkspaceStatus{}, nil
	}
	branch, err := runner.run(ctx, workingDir, "git", "branch", "--show-current")
	if err != nil {
		return WorkspaceStatus{}, fmt.Errorf("reading Session git branch: %w", err)
	}
	if branch == "" {
		return WorkspaceStatus{}, nil
	}
	status := WorkspaceStatus{Branch: branch}
	if !forceDiscovery && branch == previous.Branch && reusablePullRequest(previous.PullRequest) {
		status.PullRequest, err = readPullRequestStatus(ctx, runner, workingDir, previous.PullRequest.URL)
		if err != nil {
			status.PullRequest = previous.PullRequest
		}
		return status, err
	}
	repository, headOwner, err := repositoryContext(ctx, runner, workingDir, branch)
	if err != nil {
		return status, err
	}
	status.PullRequest, err = findPullRequest(ctx, runner, workingDir, branch, repository, headOwner)
	if err != nil {
		return status, err
	}
	if status.PullRequest == nil {
		if upstreamRepo := environmentValue(environment, "KELOS_UPSTREAM_REPO"); upstreamRepo != "" {
			repository, parseErr := repositoryFromName(repository.Host, upstreamRepo)
			if parseErr != nil {
				return status, parseErr
			}
			status.PullRequest, err = findPullRequest(ctx, runner, workingDir, branch, repository, headOwner)
			if err != nil {
				return status, err
			}
		}
	}
	return status, nil
}

func reusablePullRequest(pullRequest *kelos.SessionPullRequest) bool {
	return pullRequest != nil && pullRequest.State != kelos.SessionPullRequestStateMerged && pullRequest.State != kelos.SessionPullRequestStateClosed
}

func repositoryContext(ctx context.Context, runner workspaceStatusRunner, workingDir, branch string) (githubRepository, string, error) {
	remote, err := runner.run(ctx, workingDir, "git", "for-each-ref", "--format=%(push:remotename)", "refs/heads/"+branch)
	if err != nil {
		return githubRepository{}, "", fmt.Errorf("reading Session branch push remote: %w", err)
	}
	if remote == "" {
		remote = "origin"
	}
	remoteURL, err := runner.run(ctx, workingDir, "git", "remote", "get-url", remote)
	if err != nil {
		return githubRepository{}, "", fmt.Errorf("reading Session repository URL: %w", err)
	}
	repository, err := repositoryFromRemoteURL(remoteURL)
	if err != nil {
		return githubRepository{}, "", fmt.Errorf("identifying Session repository: %w", err)
	}
	remoteURL, err = runner.run(ctx, workingDir, "git", "remote", "get-url", "--push", remote)
	if err != nil {
		return githubRepository{}, "", fmt.Errorf("reading Session branch push URL: %w", err)
	}
	pushRepository, err := repositoryFromRemoteURL(remoteURL)
	if err != nil {
		return githubRepository{}, "", fmt.Errorf("identifying Session branch push repository: %w", err)
	}
	return repository, pushRepository.Owner, nil
}

func repositoryFromRemoteURL(remoteURL string) (githubRepository, error) {
	repositoryPath := remoteURL
	host := ""
	parsed, err := url.Parse(remoteURL)
	if err == nil && parsed.Scheme != "" {
		repositoryPath = parsed.Path
		host = parsed.Hostname()
	} else if prefix, path, ok := strings.Cut(remoteURL, ":"); ok && strings.Contains(prefix, "@") {
		repositoryPath = path
		host = strings.TrimPrefix(prefix, "git@")
	}
	repositoryPath = strings.Trim(strings.TrimSuffix(repositoryPath, ".git"), "/")
	parts := strings.Split(repositoryPath, "/")
	if host == "" || len(parts) < 2 || parts[len(parts)-2] == "" || parts[len(parts)-1] == "" {
		return githubRepository{}, fmt.Errorf("invalid remote URL %q", remoteURL)
	}
	return githubRepository{Host: host, Owner: parts[len(parts)-2], Name: parts[len(parts)-1]}, nil
}

func repositoryFromName(host, name string) (githubRepository, error) {
	parts := strings.Split(strings.Trim(name, "/"), "/")
	if host == "" || len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return githubRepository{}, fmt.Errorf("invalid GitHub repository %q", name)
	}
	return githubRepository{Host: host, Owner: parts[0], Name: parts[1]}, nil
}

func findPullRequest(ctx context.Context, runner workspaceStatusRunner, workingDir, branch string, repository githubRepository, headOwner string) (*kelos.SessionPullRequest, error) {
	endpoint := fmt.Sprintf("repos/%s/%s/pulls", repository.Owner, repository.Name)
	output, err := runner.run(ctx, workingDir, "gh", "api", "--hostname", repository.Host, "--method", "GET", endpoint, "-f", "state=all", "-f", "head="+headOwner+":"+branch, "-F", "per_page=1")
	if err != nil {
		return nil, fmt.Errorf("listing pull requests: %w", err)
	}
	if output == "" {
		return nil, nil
	}
	var pullRequests []struct {
		URL      string  `json:"html_url"`
		State    string  `json:"state"`
		Draft    bool    `json:"draft"`
		MergedAt *string `json:"merged_at"`
	}
	if err := json.Unmarshal([]byte(output), &pullRequests); err != nil {
		return nil, fmt.Errorf("decoding pull requests: %w", err)
	}
	if len(pullRequests) == 0 {
		return nil, nil
	}
	pullRequest := pullRequests[0]
	state := strings.ToUpper(pullRequest.State)
	if pullRequest.MergedAt != nil {
		state = "MERGED"
	}
	fallbackState, err := sessionPullRequestState(state, pullRequest.Draft)
	if err != nil {
		return nil, err
	}
	fallback := &kelos.SessionPullRequest{URL: pullRequest.URL, State: fallbackState}
	status, err := readPullRequestStatus(ctx, runner, workingDir, pullRequest.URL)
	if err != nil {
		return fallback, err
	}
	return status, nil
}

func readPullRequestStatus(ctx context.Context, runner workspaceStatusRunner, workingDir, pullRequestURL string) (*kelos.SessionPullRequest, error) {
	repository, number, err := pullRequestCoordinatesFromURL(pullRequestURL)
	if err != nil {
		return nil, err
	}
	output, err := runner.run(ctx, workingDir, "gh", "api", "--hostname", repository.Host, "graphql", "-f", "owner="+repository.Owner, "-f", "repo="+repository.Name, "-F", "number="+strconv.Itoa(number), "-f", "query="+pullRequestStatusQuery)
	if err != nil {
		return nil, fmt.Errorf("reading pull request status: %w", err)
	}
	var response struct {
		Data struct {
			Repository struct {
				PullRequest *githubPullRequestStatus `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(output), &response); err != nil {
		return nil, fmt.Errorf("decoding pull request status: %w", err)
	}
	pullRequest := response.Data.Repository.PullRequest
	if pullRequest == nil {
		return nil, fmt.Errorf("pull request %s not found", pullRequestURL)
	}
	state, err := sessionPullRequestState(pullRequest.State, pullRequest.IsDraft)
	if err != nil {
		return nil, err
	}
	var checks []githubStatusCheck
	if len(pullRequest.Commits.Nodes) > 0 {
		if rollup := pullRequest.Commits.Nodes[0].Commit.StatusCheckRollup; rollup != nil {
			checks = rollup.Contexts.Nodes
		}
	}
	if pullRequest.MergeQueueEntry != nil {
		state = kelos.SessionPullRequestStateQueued
		checks = nil
		if pullRequest.MergeQueueEntry.HeadCommit != nil && pullRequest.MergeQueueEntry.HeadCommit.StatusCheckRollup != nil {
			checks = pullRequest.MergeQueueEntry.HeadCommit.StatusCheckRollup.Contexts.Nodes
		}
	}
	return &kelos.SessionPullRequest{
		URL:    pullRequest.URL,
		State:  state,
		Checks: sessionPullRequestChecks(checks),
	}, nil
}

func pullRequestCoordinatesFromURL(pullRequestURL string) (githubRepository, int, error) {
	parsed, err := url.Parse(pullRequestURL)
	if err != nil || parsed.Hostname() == "" {
		return githubRepository{}, 0, fmt.Errorf("invalid pull request URL %q", pullRequestURL)
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 4 || parts[2] != "pull" {
		return githubRepository{}, 0, fmt.Errorf("invalid pull request URL %q", pullRequestURL)
	}
	number, err := strconv.Atoi(parts[3])
	if err != nil || number < 1 {
		return githubRepository{}, 0, fmt.Errorf("invalid pull request URL %q", pullRequestURL)
	}
	return githubRepository{Host: parsed.Hostname(), Owner: parts[0], Name: parts[1]}, number, nil
}

func sessionPullRequestChecks(checks []githubStatusCheck) *kelos.SessionPullRequestChecks {
	if len(checks) == 0 {
		return nil
	}

	result := &kelos.SessionPullRequestChecks{
		State: kelos.SessionPullRequestChecksStateSuccess,
		Total: int32(len(checks)),
	}
	for _, check := range checks {
		completed, failed := completedGitHubCheck(check)
		if completed {
			result.Completed++
		}
		if failed {
			result.State = kelos.SessionPullRequestChecksStateFailure
		}
	}
	if result.State != kelos.SessionPullRequestChecksStateFailure && result.Completed < result.Total {
		result.State = kelos.SessionPullRequestChecksStatePending
	}
	return result
}

func completedGitHubCheck(check githubStatusCheck) (completed, failed bool) {
	switch check.TypeName {
	case "CheckRun":
		if !strings.EqualFold(check.Status, "COMPLETED") {
			return false, false
		}
		switch strings.ToUpper(check.Conclusion) {
		case "SUCCESS", "NEUTRAL", "SKIPPED":
			return true, false
		case "ACTION_REQUIRED", "CANCELLED", "FAILURE", "STALE", "STARTUP_FAILURE", "TIMED_OUT":
			return true, true
		default:
			return false, false
		}
	case "StatusContext":
		switch strings.ToUpper(check.State) {
		case "SUCCESS":
			return true, false
		case "ERROR", "FAILURE":
			return true, true
		default:
			return false, false
		}
	default:
		return false, false
	}
}

func sessionPullRequestState(state string, draft bool) (kelos.SessionPullRequestState, error) {
	switch strings.ToUpper(state) {
	case "OPEN":
		if draft {
			return kelos.SessionPullRequestStateDraft, nil
		}
		return kelos.SessionPullRequestStateOpen, nil
	case "MERGED":
		return kelos.SessionPullRequestStateMerged, nil
	case "CLOSED":
		return kelos.SessionPullRequestStateClosed, nil
	default:
		return "", fmt.Errorf("unsupported pull request state %q", state)
	}
}

func environmentValue(environment []string, name string) string {
	prefix := name + "="
	for _, entry := range environment {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	return ""
}

func refreshWorkspaceStatus(ctx context.Context, stateDir, workingDir string, environment []string, forceDiscovery bool) (WorkspaceStatus, error) {
	return refreshWorkspaceStatusWithRunner(ctx, realWorkspaceStatusRunner{}, stateDir, workingDir, environment, forceDiscovery)
}

func refreshWorkspaceStatusWithRunner(ctx context.Context, runner workspaceStatusRunner, stateDir, workingDir string, environment []string, forceDiscovery bool) (WorkspaceStatus, error) {
	previous, err := readRecordedWorkspaceStatus(stateDir)
	if err != nil {
		return WorkspaceStatus{}, err
	}
	status, captureErr := captureWorkspaceStatusFromPrevious(ctx, runner, workingDir, environment, previous, forceDiscovery)
	if captureErr != nil {
		if status.Branch == "" {
			return previous, captureErr
		}
		if status.Branch == previous.Branch {
			if status.PullRequest == nil {
				status.PullRequest = previous.PullRequest
			} else if previous.PullRequest != nil && status.PullRequest.URL == previous.PullRequest.URL && status.PullRequest.Checks == nil {
				status.PullRequest.Checks = previous.PullRequest.Checks
			}
		}
	}
	if err := writeWorkspaceStatus(stateDir, status); err != nil {
		return status, err
	}
	return status, captureErr
}

func writeWorkspaceStatus(stateDir string, status WorkspaceStatus) error {
	file, err := os.CreateTemp(stateDir, ".workspace-status-*")
	if err != nil {
		return fmt.Errorf("creating Session workspace status: %w", err)
	}
	path := file.Name()
	defer os.Remove(path)
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return fmt.Errorf("securing Session workspace status: %w", err)
	}
	if err := json.NewEncoder(file).Encode(status); err != nil {
		file.Close()
		return fmt.Errorf("encoding Session workspace status: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("closing Session workspace status: %w", err)
	}
	if err := os.Rename(path, filepath.Join(stateDir, workspaceStatusFileName)); err != nil {
		return fmt.Errorf("recording Session workspace status: %w", err)
	}
	return nil
}

func readWorkspaceStatus(ctx context.Context, runner workspaceStatusRunner, stateDir, workingDir string) (WorkspaceStatus, error) {
	status, err := readRecordedWorkspaceStatus(stateDir)
	if err != nil {
		return WorkspaceStatus{}, err
	}

	isGitWorkspace, err := isGitWorkspace(ctx, runner, workingDir)
	if err != nil {
		return WorkspaceStatus{}, err
	}
	if !isGitWorkspace {
		return WorkspaceStatus{}, nil
	}
	branch, err := runner.run(ctx, workingDir, "git", "branch", "--show-current")
	if err != nil {
		return WorkspaceStatus{}, fmt.Errorf("reading Session git branch: %w", err)
	}
	if branch != status.Branch {
		status.Branch = branch
		status.PullRequest = nil
	}
	return status, nil
}

func isGitWorkspace(ctx context.Context, runner workspaceStatusRunner, workingDir string) (bool, error) {
	output, err := runner.run(ctx, workingDir, "git", "rev-parse", "--is-inside-work-tree")
	if err == nil {
		return output == "true", nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && bytes.Contains(exitError.Stderr, []byte("not a git repository")) {
		return false, nil
	}
	return false, fmt.Errorf("checking Session git workspace: %w", err)
}

func readRecordedWorkspaceStatus(stateDir string) (WorkspaceStatus, error) {
	status := WorkspaceStatus{}
	data, err := os.ReadFile(filepath.Join(stateDir, workspaceStatusFileName))
	if err == nil {
		if err := json.Unmarshal(data, &status); err != nil {
			return WorkspaceStatus{}, fmt.Errorf("decoding Session workspace status: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return WorkspaceStatus{}, fmt.Errorf("reading Session workspace status: %w", err)
	}
	return status, nil
}
