package controller

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

const (
	SpawnerImageRepository = "ghcr.io/kelos-dev/kelos-spawner"

	// DefaultSpawnerImage is the default image for the spawner binary.
	DefaultSpawnerImage = SpawnerImageRepository + ":latest"

	// SpawnerBeadsImageRepository holds the spawner variant that carries the
	// beads CLI and git. The default spawner image is distroless and can run
	// neither, so beads-sourced spawners need this one.
	SpawnerBeadsImageRepository = "ghcr.io/kelos-dev/kelos-spawner-beads"

	// DefaultSpawnerBeadsImage is the default image for beads-sourced spawners.
	DefaultSpawnerBeadsImage = SpawnerBeadsImageRepository + ":latest"

	// SpawnerServiceAccount is the service account used by spawner Deployments.
	SpawnerServiceAccount = "kelos-spawner"

	// SpawnerClusterRole is the ClusterRole referenced by spawner RoleBindings.
	SpawnerClusterRole = "kelos-spawner-role"

	// BeadsWorkDir is where a beads-sourced spawner keeps its clone of the
	// Dolt remote. It doubles as HOME so the beads CLI has a writable place
	// for its own Dolt state.
	BeadsWorkDir = "/beads"

	// BeadsWorkDirVolume names the emptyDir backing BeadsWorkDir.
	BeadsWorkDirVolume = "beads-workdir"
)

// DeploymentBuilder constructs Kubernetes Deployments for TaskSpawners.
type DeploymentBuilder struct {
	SpawnerImage           string
	SpawnerBeadsImage      string
	SpawnerImagePullPolicy corev1.PullPolicy
	SpawnerResources       *corev1.ResourceRequirements
}

// NewDeploymentBuilder creates a new DeploymentBuilder.
func NewDeploymentBuilder() *DeploymentBuilder {
	return &DeploymentBuilder{
		SpawnerImage:      DefaultSpawnerImage,
		SpawnerBeadsImage: DefaultSpawnerBeadsImage,
	}
}

// spawnerImageFor returns the image that runs this TaskSpawner's spawner
// container. A beads source needs the beads CLI and git, which the default
// distroless image does not carry, so it gets the beads variant instead.
func (b *DeploymentBuilder) spawnerImageFor(ts *kelos.TaskSpawner) string {
	if ts.Spec.When.Beads != nil {
		return b.SpawnerBeadsImage
	}
	return b.SpawnerImage
}

// spawnerPodParts holds the components needed to build a spawner pod template.
type spawnerPodParts struct {
	args         []string
	envVars      []corev1.EnvVar
	labels       map[string]string
	volumes      []corev1.Volume
	volumeMounts []corev1.VolumeMount
}

// buildPodParts computes the args, env, volumes, and labels that are shared
// between a Deployment pod and a CronJob pod for the given TaskSpawner.
func (b *DeploymentBuilder) buildPodParts(ts *kelos.TaskSpawner, workspace *kelos.WorkspaceSpec, isGitHubApp bool) spawnerPodParts {
	args := []string{
		"--taskspawner-name=" + ts.Name,
		"--taskspawner-namespace=" + ts.Namespace,
	}

	var envVars []corev1.EnvVar

	if workspace != nil {
		host, owner, repo := parseGitHubRepo(workspace.Repo)

		// Override with an explicit GitHub source repo if set (fork workflow).
		if repoOverride := githubSourceRepoOverride(ts); repoOverride != "" {
			overrideHost, overrideOwner, overrideRepo := parseGitHubRepo(repoOverride)
			owner = overrideOwner
			repo = overrideRepo
			// Only override the host when the override itself provides one.
			// Shorthand "owner/repo" returns an empty host from parseGitHubRepo;
			// in that case keep the workspace host so GHES API URLs are preserved.
			if overrideHost != "" {
				host = overrideHost
			}
		}

		args = append(args,
			"--github-owner="+owner,
			"--github-repo="+repo,
		)
		if workspaceUsesGHProxy(workspace) && ts.Spec.TaskTemplate.WorkspaceRef != nil {
			args = append(args, "--gh-proxy-url="+WorkspaceGHProxyServiceURL(ts.Namespace, ts.Spec.TaskTemplate.WorkspaceRef.Name))
		}
		if apiBaseURL := gitHubAPIBaseURL(host); apiBaseURL != "" {
			args = append(args, "--github-api-base-url="+apiBaseURL)
		}
		if workspace.SecretRef != nil && taskSpawnerNeedsGitHubToken(ts, workspaceUsesGHProxy(workspace)) {
			if isGitHubApp {
				// GitHub App: inject credentials as env vars for in-process token generation
				envVars = append(envVars,
					corev1.EnvVar{
						Name: "GITHUB_APP_ID",
						ValueFrom: &corev1.EnvVarSource{
							SecretKeyRef: &corev1.SecretKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: workspace.SecretRef.Name,
								},
								Key: "appID",
							},
						},
					},
					corev1.EnvVar{
						Name: "GITHUB_APP_INSTALLATION_ID",
						ValueFrom: &corev1.EnvVarSource{
							SecretKeyRef: &corev1.SecretKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: workspace.SecretRef.Name,
								},
								Key: "installationID",
							},
						},
					},
					corev1.EnvVar{
						Name: "GITHUB_APP_PRIVATE_KEY",
						ValueFrom: &corev1.EnvVarSource{
							SecretKeyRef: &corev1.SecretKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: workspace.SecretRef.Name,
								},
								Key: "privateKey",
							},
						},
					},
				)
			} else {
				// PAT: inject GITHUB_TOKEN from secret
				envVars = append(envVars, corev1.EnvVar{
					Name: "GITHUB_TOKEN",
					ValueFrom: &corev1.EnvVarSource{
						SecretKeyRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: workspace.SecretRef.Name,
							},
							Key: "GITHUB_TOKEN",
						},
					},
				})
			}
		}
	}

	if ts.Spec.When.Jira != nil {
		jira := ts.Spec.When.Jira
		args = append(args,
			"--jira-base-url="+jira.BaseURL,
			"--jira-project="+jira.Project,
		)
		if jira.JQL != "" {
			args = append(args, "--jira-jql="+jira.JQL)
		}

		// JIRA_USER is optional: when present, Jira Cloud basic auth is used
		// (email + API token). When absent, Bearer token auth is used for
		// Jira Data Center/Server PATs.
		optional := true
		envVars = append(envVars,
			corev1.EnvVar{
				Name: "JIRA_USER",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: jira.SecretRef.Name,
						},
						Key:      "JIRA_USER",
						Optional: &optional,
					},
				},
			},
			corev1.EnvVar{
				Name: "JIRA_TOKEN",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: jira.SecretRef.Name,
						},
						Key: "JIRA_TOKEN",
					},
				},
			},
		)
	}

	var volumes []corev1.Volume
	var volumeMounts []corev1.VolumeMount

	if ts.Spec.When.Beads != nil {
		beads := ts.Spec.When.Beads

		// The beads CLI clones the Dolt remote and auto-starts a local Dolt
		// server, so it needs a writable working directory and a writable HOME
		// (it keeps per-user Dolt state under $HOME/.dolt, and the spawner
		// runs as a non-root user with no home directory of its own).
		volumes = append(volumes, corev1.Volume{
			Name:         BeadsWorkDirVolume,
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		})
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      BeadsWorkDirVolume,
			MountPath: BeadsWorkDir,
		})

		// BEADS_DOLT_USER is optional: the CLI defaults it when the Secret
		// carries only a password.
		optional := true
		envVars = append(envVars,
			corev1.EnvVar{Name: "HOME", Value: BeadsWorkDir},
			corev1.EnvVar{Name: "BEADS_WORKDIR", Value: BeadsWorkDir},
			// Keep the CLI from prompting: a spawner has no terminal.
			corev1.EnvVar{Name: "BD_NON_INTERACTIVE", Value: "1"},
			corev1.EnvVar{
				Name: "BEADS_DOLT_USER",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: beads.SecretRef.Name,
						},
						Key:      "BEADS_DOLT_USER",
						Optional: &optional,
					},
				},
			},
			corev1.EnvVar{
				Name: "BEADS_DOLT_PASSWORD",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: beads.SecretRef.Name,
						},
						Key: "BEADS_DOLT_PASSWORD",
					},
				},
			},
		)
	}

	labels := map[string]string{
		"kelos.dev/name":        "kelos",
		"kelos.dev/component":   "spawner",
		"kelos.dev/managed-by":  "kelos-controller",
		"kelos.dev/taskspawner": ts.Name,
	}

	return spawnerPodParts{
		args:         args,
		envVars:      envVars,
		labels:       labels,
		volumes:      volumes,
		volumeMounts: volumeMounts,
	}
}

// Build creates a Deployment for the given TaskSpawner.
// The workspace parameter provides the repository URL and optional secretRef
// for GitHub API authentication. The isGitHubApp parameter indicates whether
// the workspace secret contains GitHub App credentials, which are injected as
// env vars for in-process token generation.
func (b *DeploymentBuilder) Build(ts *kelos.TaskSpawner, workspace *kelos.WorkspaceSpec, isGitHubApp bool) *appsv1.Deployment {
	replicas := int32(1)
	p := b.buildPodParts(ts, workspace, isGitHubApp)

	spawnerContainer := corev1.Container{
		Name:            "spawner",
		Image:           b.spawnerImageFor(ts),
		ImagePullPolicy: b.SpawnerImagePullPolicy,
		Args:            p.args,
		Env:             p.envVars,
		VolumeMounts:    p.volumeMounts,
		Ports: []corev1.ContainerPort{
			{
				Name:          "metrics",
				ContainerPort: 8080,
				Protocol:      corev1.ProtocolTCP,
			},
		},
	}
	if b.SpawnerResources != nil {
		spawnerContainer.Resources = *b.SpawnerResources
	}

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ts.Name,
			Namespace: ts.Namespace,
			Labels:    p.labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: p.labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: p.labels,
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: SpawnerServiceAccount,
					RestartPolicy:      corev1.RestartPolicyAlways,
					Containers:         []corev1.Container{spawnerContainer},
					Volumes:            p.volumes,
				},
			},
		},
	}
}

// BuildCronJob creates a CronJob for a cron-based TaskSpawner.
// Instead of running a long-lived Deployment with pollInterval, the CronJob
// runs the spawner in one-shot mode on the cron schedule itself.
// The workspace and isGitHubApp parameters are passed through to buildPodParts
// so that CronJob pods get the same GitHub auth and repo args as Deployments.
func (b *DeploymentBuilder) BuildCronJob(ts *kelos.TaskSpawner, workspace *kelos.WorkspaceSpec, isGitHubApp bool) *batchv1.CronJob {
	p := b.buildPodParts(ts, workspace, isGitHubApp)

	// Add --one-shot flag so the spawner runs a single cycle and exits.
	// Copy to avoid mutating the shared slice from buildPodParts.
	args := make([]string, len(p.args), len(p.args)+1)
	copy(args, p.args)
	args = append(args, "--one-shot")

	spawnerContainer := corev1.Container{
		Name:            "spawner",
		Image:           b.spawnerImageFor(ts),
		ImagePullPolicy: b.SpawnerImagePullPolicy,
		Args:            args,
		Env:             p.envVars,
	}
	if b.SpawnerResources != nil {
		spawnerContainer.Resources = *b.SpawnerResources
	}

	backoffLimit := int32(0)
	// Keep the last 3 successful and 1 failed jobs for debugging.
	successfulJobsHistoryLimit := int32(3)
	failedJobsHistoryLimit := int32(1)

	return &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ts.Name,
			Namespace: ts.Namespace,
			Labels:    p.labels,
		},
		Spec: batchv1.CronJobSpec{
			Schedule:                   ts.Spec.When.Cron.Schedule,
			ConcurrencyPolicy:          batchv1.ForbidConcurrent,
			SuccessfulJobsHistoryLimit: &successfulJobsHistoryLimit,
			FailedJobsHistoryLimit:     &failedJobsHistoryLimit,
			JobTemplate: batchv1.JobTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: p.labels,
				},
				Spec: batchv1.JobSpec{
					BackoffLimit: &backoffLimit,
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: p.labels,
						},
						Spec: corev1.PodSpec{
							ServiceAccountName: SpawnerServiceAccount,
							RestartPolicy:      corev1.RestartPolicyNever,
							Containers:         []corev1.Container{spawnerContainer},
						},
					},
				},
			},
		},
	}
}

// httpsRepoRe matches HTTPS-style repository URLs: https://host/owner/repo
var httpsRepoRe = regexp.MustCompile(`https?://([^/]+)/([^/]+)/([^/.]+)`)

// sshRepoRe matches SSH-style repository URLs: git@host:owner/repo
var sshRepoRe = regexp.MustCompile(`git@([^:]+):([^/]+)/([^/.]+)`)

// parseGitHubRepo extracts the host, owner, and repo from a GitHub repository URL.
// Supports HTTPS (https://github.com/owner/repo.git) and SSH (git@github.com:owner/repo.git)
// for both github.com and GitHub Enterprise hosts.
func parseGitHubRepo(repoURL string) (host, owner, repo string) {
	repoURL = strings.TrimSuffix(repoURL, ".git")

	if m := httpsRepoRe.FindStringSubmatch(repoURL); len(m) == 4 {
		host := m[1]
		if parsed, err := url.Parse(repoURL); err == nil && parsed.Host != "" {
			host = parsed.Host
		}
		return host, m[2], m[3]
	}
	if m := sshRepoRe.FindStringSubmatch(repoURL); len(m) == 4 {
		return m[1], m[2], m[3]
	}

	// Fallback: try splitting by '/' and taking last two segments
	parts := strings.Split(strings.TrimSuffix(repoURL, "/"), "/")
	if len(parts) >= 2 {
		return "", parts[len(parts)-2], parts[len(parts)-1]
	}

	return "", "", fmt.Sprintf("unknown-repo-%s", repoURL)
}

// parseGitHubOwnerRepo extracts owner and repo from a GitHub repository URL.
// Supports HTTPS (https://github.com/owner/repo.git) and SSH (git@github.com:owner/repo.git).
func parseGitHubOwnerRepo(repoURL string) (owner, repo string) {
	_, owner, repo = parseGitHubRepo(repoURL)
	return owner, repo
}

func githubSourceRepoOverride(ts *kelos.TaskSpawner) string {
	if ts.Spec.When.GitHubIssues != nil && ts.Spec.When.GitHubIssues.Repo != "" {
		return ts.Spec.When.GitHubIssues.Repo
	}
	if ts.Spec.When.GitHubPullRequests != nil && ts.Spec.When.GitHubPullRequests.Repo != "" {
		return ts.Spec.When.GitHubPullRequests.Repo
	}
	return ""
}

func taskSpawnerNeedsGitHubToken(ts *kelos.TaskSpawner, ghProxyConfigured bool) bool {
	if ts.Spec.When.GitHubIssues != nil {
		return !ghProxyConfigured || gitHubReportingNeedsToken(ts.Spec.When.GitHubIssues.Reporting)
	}
	if ts.Spec.When.GitHubPullRequests != nil {
		return !ghProxyConfigured || gitHubReportingNeedsToken(ts.Spec.When.GitHubPullRequests.Reporting)
	}
	return false
}

func gitHubReportingNeedsToken(reporting *kelos.GitHubReporting) bool {
	return reporting != nil && (reporting.Enabled || reporting.Comments != nil || reporting.Checks != nil)
}

func workspaceUsesGHProxy(workspace *kelos.WorkspaceSpec) bool {
	return workspace != nil && workspace.GHProxy != nil
}

func validateWorkspaceGHProxyRepoOverride(ts *kelos.TaskSpawner, workspace *kelos.WorkspaceSpec) error {
	if workspace == nil {
		return nil
	}
	repoOverride := githubSourceRepoOverride(ts)
	if repoOverride == "" {
		return nil
	}

	workspaceHost, _, _ := parseGitHubRepo(workspace.Repo)
	overrideHost, _, _ := parseGitHubRepo(repoOverride)
	if overrideHost == "" || workspaceHost == "" || overrideHost == workspaceHost {
		return nil
	}

	return fmt.Errorf("github source repo override host %q does not match Workspace host %q", overrideHost, workspaceHost)
}

// ParseResourceList parses a comma-separated "name=value" string into a
// corev1.ResourceList. An empty string returns nil. Each value must pass
// Kubernetes quantity parsing.
func ParseResourceList(s string) (corev1.ResourceList, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	rl := corev1.ResourceList{}
	for _, entry := range strings.Split(s, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid resource entry %q: expected name=value", entry)
		}
		name := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		if name == "" || value == "" {
			return nil, fmt.Errorf("invalid resource entry %q: expected name=value", entry)
		}
		qty, err := resource.ParseQuantity(value)
		if err != nil {
			return nil, fmt.Errorf("invalid quantity for %q: %w", name, err)
		}
		rl[corev1.ResourceName(name)] = qty
	}
	return rl, nil
}

// gitHubAPIBaseURL returns the GitHub API base URL for the given host.
// For github.com (or empty host) it returns an empty string, as the spawner uses the default API endpoint.
// For GitHub Enterprise hosts it returns "https://<host>/api/v3".
func gitHubAPIBaseURL(host string) string {
	if host == "" || host == "github.com" {
		return ""
	}
	return (&url.URL{Scheme: "https", Host: host, Path: "/api/v3"}).String()
}
