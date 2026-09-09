package e2e

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/test/e2e/framework"
)

var _ = Describe("TaskSpawner", func() {
	f := framework.NewFramework("spawner")

	BeforeEach(func() {
		if githubToken == "" {
			Skip("GITHUB_TOKEN not set, skipping TaskSpawner e2e tests")
		}
	})

	// This test requires at least one open GitHub issue in kelos-dev/kelos
	// with the "do-not-remove/e2e-anchor" label. See issue #117.
	It("should create a spawner Deployment and discover issues", func() {
		By("creating GitHub token secret")
		f.CreateSecret("github-token",
			"GITHUB_TOKEN="+githubToken)

		By("creating OAuth credentials secret")
		f.CreateSecret("claude-credentials",
			"CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)

		By("creating a Workspace resource with secretRef")
		f.CreateWorkspace(&kelos.Workspace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "e2e-spawner-workspace",
			},
			Spec: kelos.WorkspaceSpec{
				Repo:      "https://github.com/kelos-dev/kelos.git",
				Ref:       "main",
				SecretRef: &kelos.SecretReference{Name: "github-token"},
			},
		})

		By("creating a TaskSpawner")
		f.CreateTaskSpawner(&kelos.TaskSpawner{
			ObjectMeta: metav1.ObjectMeta{
				Name: "spawner",
			},
			Spec: kelos.TaskSpawnerSpec{
				When: kelos.When{
					GitHubIssues: &kelos.GitHubIssues{
						Labels:        []string{"do-not-remove/e2e-anchor"},
						ExcludeLabels: []string{"e2e-exclude-placeholder"},
						State:         "open",
						PollInterval:  "1m",
					},
				},
				TaskTemplate: kelos.TaskTemplate{
					Type: "claude-code",
					WorkspaceRef: &kelos.WorkspaceReference{
						Name: "e2e-spawner-workspace",
					},
					Credentials: &kelos.Credentials{
						Type:      kelos.CredentialTypeOAuth,
						SecretRef: &kelos.SecretReference{Name: "claude-credentials"},
					},
					PromptTemplate: "Fix: {{.Title}}\n{{.Body}}",
				},
			},
		})

		By("waiting for Deployment to become available")
		f.WaitForDeploymentAvailable("spawner")

		By("waiting for TaskSpawner phase to become Running")
		Eventually(func() string {
			return f.GetTaskSpawnerPhase("spawner")
		}, 3*time.Minute, 2*time.Second).Should(Equal("Running"))

		By("verifying at least one Task was created")
		Eventually(func() []string {
			return f.ListTaskNames("kelos.dev/taskspawner=spawner")
		}, 3*time.Minute, 2*time.Second).ShouldNot(BeEmpty())
	})

	It("should be accessible via CLI", func() {
		By("creating a Workspace resource")
		f.CreateWorkspace(&kelos.Workspace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "e2e-spawner-workspace",
			},
			Spec: kelos.WorkspaceSpec{
				Repo: "https://github.com/kelos-dev/kelos.git",
			},
		})

		By("creating a TaskSpawner")
		f.CreateTaskSpawner(&kelos.TaskSpawner{
			ObjectMeta: metav1.ObjectMeta{
				Name: "spawner",
			},
			Spec: kelos.TaskSpawnerSpec{
				When: kelos.When{
					GitHubIssues: &kelos.GitHubIssues{
						PollInterval: "5m",
					},
				},
				TaskTemplate: kelos.TaskTemplate{
					Type: "claude-code",
					WorkspaceRef: &kelos.WorkspaceReference{
						Name: "e2e-spawner-workspace",
					},
					Credentials: &kelos.Credentials{
						Type:      kelos.CredentialTypeOAuth,
						SecretRef: &kelos.SecretReference{Name: "claude-credentials"},
					},
				},
			},
		})

		By("verifying kelos get taskspawners lists it")
		output := framework.KelosOutput("get", "taskspawners", "-n", f.Namespace)
		Expect(output).To(ContainSubstring("spawner"))

		By("verifying kelos get taskspawner shows detail")
		output = framework.KelosOutput("get", "taskspawner", "spawner", "-n", f.Namespace, "--detail")
		Expect(output).To(ContainSubstring("spawner"))
		Expect(output).To(ContainSubstring("GitHub Issues"))

		By("verifying YAML output for a single taskspawner")
		output = framework.KelosOutput("get", "taskspawner", "spawner", "-n", f.Namespace, "-o", "yaml")
		Expect(output).To(ContainSubstring("apiVersion: kelos.dev/v1alpha2"))
		Expect(output).To(ContainSubstring("kind: TaskSpawner"))
		Expect(output).To(ContainSubstring("name: spawner"))

		By("verifying JSON output for a single taskspawner")
		output = framework.KelosOutput("get", "taskspawner", "spawner", "-n", f.Namespace, "-o", "json")
		Expect(output).To(ContainSubstring(`"apiVersion": "kelos.dev/v1alpha2"`))
		Expect(output).To(ContainSubstring(`"kind": "TaskSpawner"`))
		Expect(output).To(ContainSubstring(`"name": "spawner"`))

		By("deleting the TaskSpawner")
		f.DeleteTaskSpawner("spawner")

		By("verifying it disappears from list")
		Eventually(func() string {
			return framework.KelosOutput("get", "taskspawners", "-n", f.Namespace)
		}, 30*time.Second, time.Second).ShouldNot(ContainSubstring("spawner"))
	})
})

var _ = Describe("Cron TaskSpawner", func() {
	f := framework.NewFramework("cron")

	It("should create a CronJob and discover cron ticks", func() {
		By("creating OAuth credentials secret")
		f.CreateSecret("claude-credentials",
			"CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)

		By("creating a cron TaskSpawner with every-minute schedule")
		f.CreateTaskSpawner(&kelos.TaskSpawner{
			ObjectMeta: metav1.ObjectMeta{
				Name: "cron-spawner",
			},
			Spec: kelos.TaskSpawnerSpec{
				When: kelos.When{
					Cron: &kelos.Cron{
						Schedule: "* * * * *",
					},
				},
				TaskTemplate: kelos.TaskTemplate{
					Type:  "claude-code",
					Model: claudeCodeModel,
					Credentials: &kelos.Credentials{
						Type:      kelos.CredentialTypeOAuth,
						SecretRef: &kelos.SecretReference{Name: "claude-credentials"},
					},
					PromptTemplate: "Cron triggered at {{.Time}} (schedule: {{.Schedule}}). Print 'Hello from cron'",
				},
			},
		})

		By("waiting for CronJob to be created")
		f.WaitForCronJobCreated("cron-spawner")

		By("waiting for TaskSpawner phase to become Running")
		Eventually(func() string {
			return f.GetTaskSpawnerPhase("cron-spawner")
		}, 3*time.Minute, 2*time.Second).Should(Equal("Running"))

		By("creating a standalone Task from the cron TaskSpawner")
		output := framework.KelosOutput(
			"run",
			"--from", "taskspawner/cron-spawner",
			"--name", "cron-manual-task",
			"-n", f.Namespace,
		)
		Expect(output).To(Equal("task/cron-manual-task created"))

		By("verifying the standalone Task uses the current cron context")
		manualTask, err := f.KelosClientset.ApiV1alpha2().Tasks(f.Namespace).Get(context.TODO(), "cron-manual-task", metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(manualTask.Spec.Prompt).To(ContainSubstring("Cron triggered at "))
		Expect(manualTask.Spec.Prompt).To(ContainSubstring("(schedule: * * * * *)"))
		Expect(manualTask.Labels).NotTo(HaveKey("kelos.dev/taskspawner"))
		Expect(manualTask.Annotations).To(HaveKeyWithValue("kelos.dev/created-from-taskspawner", "cron-spawner"))

		By("verifying at least one Task was created")
		Eventually(func() []string {
			return f.ListTaskNames("kelos.dev/taskspawner=cron-spawner")
		}, 3*time.Minute, 2*time.Second).ShouldNot(BeEmpty())
	})

	It("should reconcile the complete CronJob spec", func() {
		By("creating a cron TaskSpawner")
		f.CreateTaskSpawner(&kelos.TaskSpawner{
			ObjectMeta: metav1.ObjectMeta{
				Name: "cron-spec-sync",
			},
			Spec: kelos.TaskSpawnerSpec{
				When: kelos.When{
					Cron: &kelos.Cron{
						Schedule: "0 0 1 1 *",
					},
				},
				TaskTemplate: kelos.TaskTemplate{
					Worker: &kelos.WorkerSpec{
						Type:        "claude-code",
						Credentials: &kelos.Credentials{Type: kelos.CredentialTypeNone},
					},
				},
			},
		})

		By("waiting for the CronJob to be created")
		f.WaitForCronJobCreated("cron-spec-sync")

		By("introducing drift across the CronJob, Job, and Pod specs")
		cronJob, err := f.Clientset.BatchV1().CronJobs(f.Namespace).Get(context.TODO(), "cron-spec-sync", metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		wantSpec := cronJob.Spec.DeepCopy()
		cronJob.Spec.ConcurrencyPolicy = batchv1.AllowConcurrent
		failedJobsHistoryLimit := int32(9)
		cronJob.Spec.FailedJobsHistoryLimit = &failedJobsHistoryLimit
		backoffLimit := int32(4)
		cronJob.Spec.JobTemplate.Spec.BackoffLimit = &backoffLimit
		container := &cronJob.Spec.JobTemplate.Spec.Template.Spec.Containers[0]
		container.ImagePullPolicy = corev1.PullAlways
		if wantSpec.JobTemplate.Spec.Template.Spec.Containers[0].ImagePullPolicy == corev1.PullAlways {
			container.ImagePullPolicy = corev1.PullNever
		}
		_, err = f.Clientset.BatchV1().CronJobs(f.Namespace).Update(context.TODO(), cronJob, metav1.UpdateOptions{})
		Expect(err).NotTo(HaveOccurred())

		By("waiting for the complete desired spec to be restored")
		Eventually(func(g Gomega) {
			updated, getErr := f.Clientset.BatchV1().CronJobs(f.Namespace).Get(context.TODO(), "cron-spec-sync", metav1.GetOptions{})
			g.Expect(getErr).NotTo(HaveOccurred())
			g.Expect(apiequality.Semantic.DeepEqual(updated.Spec, *wantSpec)).To(BeTrue())
		}, time.Minute, time.Second).Should(Succeed())
	})

	It("should run a cron Task with one of multiple credentials", func() {
		By("creating two OAuth credential secrets")
		f.CreateSecret("claude-credentials-a",
			"CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)
		f.CreateSecret("claude-credentials-b",
			"CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)

		maxTotalTasks := int32(1)
		By("creating a cron TaskSpawner with multiple credentials")
		f.CreateTaskSpawner(&kelos.TaskSpawner{
			ObjectMeta: metav1.ObjectMeta{
				Name: "cron-credentials",
			},
			Spec: kelos.TaskSpawnerSpec{
				When: kelos.When{
					Cron: &kelos.Cron{
						Schedule: "* * * * *",
					},
				},
				TaskTemplate: kelos.TaskTemplate{
					Worker: &kelos.WorkerSpec{
						Type:  "claude-code",
						Model: claudeCodeModel,
					},
					PromptTemplate: "Reply exactly: cron credential pool works",
				},
				Credentials: []kelos.SpawnerCredential{
					{
						Name:      "account-a",
						Type:      kelos.CredentialTypeOAuth,
						SecretRef: kelos.SecretReference{Name: "claude-credentials-a"},
					},
					{
						Name:      "account-b",
						Type:      kelos.CredentialTypeOAuth,
						SecretRef: kelos.SecretReference{Name: "claude-credentials-b"},
					},
				},
				MaxTotalTasks: &maxTotalTasks,
			},
		})

		By("waiting for a cron Task to be created")
		var task kelos.Task
		Eventually(func(g Gomega) {
			tasks, err := f.KelosClientset.ApiV1alpha2().Tasks(f.Namespace).List(context.TODO(), metav1.ListOptions{
				LabelSelector: "kelos.dev/taskspawner=cron-credentials",
			})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(tasks.Items).To(HaveLen(1))
			task = tasks.Items[0]
		}, 3*time.Minute, 2*time.Second).Should(Succeed())

		By("verifying the selected credential is pinned to the Task")
		credentialSecrets := map[string]string{
			"account-a": "claude-credentials-a",
			"account-b": "claude-credentials-b",
		}
		selectedCredential, assigned := task.Labels["kelos.dev/spawner-credential"]
		Expect(assigned).To(BeTrue())
		Expect(credentialSecrets).To(HaveKey(selectedCredential))
		Expect(task.Spec.Worker).NotTo(BeNil())
		Expect(task.Spec.Worker.Credentials).NotTo(BeNil())
		Expect(task.Spec.Worker.Credentials.Type).To(Equal(kelos.CredentialTypeOAuth))
		Expect(task.Spec.Worker.Credentials.SecretRef).NotTo(BeNil())
		Expect(task.Spec.Worker.Credentials.SecretRef.Name).To(Equal(credentialSecrets[selectedCredential]))

		By("waiting for the Task to succeed with the selected credential")
		f.WaitForTaskPhase(task.Name, string(kelos.TaskPhaseSucceeded))
	})

	It("should deduplicate Tasks across cron ticks when nameTemplate is deterministic", func() {
		By("creating OAuth credentials secret")
		f.CreateSecret("claude-credentials",
			"CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)

		By("creating an every-minute cron TaskSpawner with a constant nameTemplate")
		// A constant nameTemplate renders the same Task name every tick, so after
		// the first Task all later ticks must reuse it (owned deduplication)
		// rather than create duplicates or error.
		f.CreateTaskSpawner(&kelos.TaskSpawner{
			ObjectMeta: metav1.ObjectMeta{
				Name: "cron-dedup",
			},
			Spec: kelos.TaskSpawnerSpec{
				When: kelos.When{
					Cron: &kelos.Cron{
						Schedule: "* * * * *",
					},
				},
				TaskTemplate: kelos.TaskTemplate{
					Type:  "claude-code",
					Model: claudeCodeModel,
					Credentials: &kelos.Credentials{
						Type:      kelos.CredentialTypeOAuth,
						SecretRef: &kelos.SecretReference{Name: "claude-credentials"},
					},
					NameTemplate:   "cron-dedup-fixed",
					PromptTemplate: "Print 'Hello from cron dedup'",
				},
			},
		})

		By("waiting for CronJob to be created")
		f.WaitForCronJobCreated("cron-dedup")

		By("waiting for the deterministically named Task to be created")
		Eventually(func() []string {
			return f.ListTaskNames("kelos.dev/taskspawner=cron-dedup")
		}, 3*time.Minute, 2*time.Second).Should(ConsistOf("cron-dedup-fixed"))

		By("verifying later cron ticks reuse the same Task instead of creating duplicates")
		Consistently(func() []string {
			return f.ListTaskNames("kelos.dev/taskspawner=cron-dedup")
		}, 75*time.Second, 5*time.Second).Should(ConsistOf("cron-dedup-fixed"))

		By("verifying the spawner stayed healthy (owned dedup, not an error)")
		Expect(f.GetTaskSpawnerPhase("cron-dedup")).To(Equal("Running"))
		spawner, err := f.KelosClientset.ApiV1alpha2().TaskSpawners(f.Namespace).Get(context.TODO(), "cron-dedup", metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		for _, c := range spawner.Status.Conditions {
			if c.Type == "DiscoveryError" {
				Expect(string(c.Status)).To(Equal("False"), "dedup must not raise DiscoveryError")
			}
		}
	})

	It("should be accessible via CLI with cron source info", func() {
		By("creating a cron TaskSpawner")
		f.CreateTaskSpawner(&kelos.TaskSpawner{
			ObjectMeta: metav1.ObjectMeta{
				Name: "cron-spawner",
			},
			Spec: kelos.TaskSpawnerSpec{
				When: kelos.When{
					Cron: &kelos.Cron{
						Schedule: "0 9 * * 1",
					},
				},
				TaskTemplate: kelos.TaskTemplate{
					Type: "claude-code",
					Credentials: &kelos.Credentials{
						Type:      kelos.CredentialTypeOAuth,
						SecretRef: &kelos.SecretReference{Name: "claude-credentials"},
					},
				},
			},
		})

		By("verifying kelos get taskspawners lists it")
		output := framework.KelosOutput("get", "taskspawners", "-n", f.Namespace)
		Expect(output).To(ContainSubstring("cron-spawner"))

		By("verifying kelos get taskspawner shows cron detail")
		output = framework.KelosOutput("get", "taskspawner", "cron-spawner", "-n", f.Namespace, "--detail")
		Expect(output).To(ContainSubstring("cron-spawner"))
		Expect(output).To(ContainSubstring("Cron"))
		Expect(output).To(ContainSubstring("0 9 * * 1"))

		By("deleting the TaskSpawner")
		f.DeleteTaskSpawner("cron-spawner")

		By("verifying it disappears from list")
		Eventually(func() string {
			return framework.KelosOutput("get", "taskspawners", "-n", f.Namespace)
		}, 30*time.Second, time.Second).ShouldNot(ContainSubstring("cron-spawner"))
	})
})

var _ = Describe("get taskspawner", func() {
	It("should succeed with 'taskspawners' alias", func() {
		framework.KelosOutput("get", "taskspawners")
	})

	It("should succeed with 'ts' alias", func() {
		framework.KelosOutput("get", "ts")
	})

	It("should succeed with 'taskspawner' subcommand", func() {
		framework.KelosOutput("get", "taskspawner")
	})

	It("should fail for a nonexistent taskspawner", func() {
		framework.KelosFail("get", "taskspawner", "nonexistent-spawner")
	})

	It("should output taskspawner list in YAML format", func() {
		output := framework.KelosOutput("get", "taskspawners", "-o", "yaml")
		Expect(output).To(ContainSubstring("apiVersion: kelos.dev/v1alpha2"))
		Expect(output).To(ContainSubstring("kind: TaskSpawnerList"))
	})

	It("should output taskspawner list in JSON format", func() {
		output := framework.KelosOutput("get", "taskspawners", "-o", "json")
		Expect(output).To(ContainSubstring(`"apiVersion": "kelos.dev/v1alpha2"`))
		Expect(output).To(ContainSubstring(`"kind": "TaskSpawnerList"`))
	})

	It("should fail with unknown output format", func() {
		framework.KelosFail("get", "taskspawners", "-o", "invalid")
	})
})
