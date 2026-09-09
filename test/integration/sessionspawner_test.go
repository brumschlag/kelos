package integration

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

var _ = Describe("SessionSpawner", func() {
	var namespace string

	BeforeEach(func() {
		namespace = fmt.Sprintf("sessionspawner-%d", time.Now().UnixNano())
		Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})).To(Succeed())
	})

	It("accepts a configured GitHub webhook", func() {
		spawner := validSessionSpawner(namespace, "workers")
		Expect(k8sClient.Create(ctx, spawner)).To(Succeed())

		Eventually(func(g Gomega) {
			var current kelos.SessionSpawner
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(spawner), &current)).To(Succeed())
			g.Expect(current.Status.ObservedGeneration).To(Equal(current.Generation))
			g.Expect(current.Status.TotalSessions).To(Equal(int32(0)))
			g.Expect(current.Status.Conditions).To(BeEmpty())
		}, 10*time.Second, 100*time.Millisecond).Should(Succeed())
	})

	It("rejects a SessionSpawner without spec", func() {
		spawner := &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "kelos.dev/v1alpha2",
			"kind":       "SessionSpawner",
			"metadata": map[string]interface{}{
				"name":      "missing-spec",
				"namespace": namespace,
			},
		}}
		spawner.SetGroupVersionKind(schema.GroupVersionKind{Group: "kelos.dev", Version: "v1alpha2", Kind: "SessionSpawner"})
		err := k8sClient.Create(ctx, spawner)
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "error: %v", err)
	})

	It("accepts GitHub event types other than issue_comment", func() {
		spawner := validSessionSpawner(namespace, "issues-events")
		spawner.Spec.When.GitHubWebhook.Events = []string{"issues"}
		spawner.Spec.When.GitHubWebhook.Filters[0].Event = "issues"
		spawner.Spec.When.GitHubWebhook.Filters[0].Action = "opened"
		spawner.Spec.When.GitHubWebhook.Filters[0].BodyPattern = ""
		Expect(k8sClient.Create(ctx, spawner)).To(Succeed())
	})

	It("rejects GitHub reporting until Session reporting is supported", func() {
		spawner := validSessionSpawner(namespace, "reporting")
		spawner.Spec.When.GitHubWebhook.Reporting = &kelos.GitHubReporting{Enabled: true}
		err := k8sClient.Create(ctx, spawner)
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "error: %v", err)
	})

	It("requires a templated initialPrompt", func() {
		spawner := validSessionSpawner(namespace, "missing-prompt")
		spawner.Spec.SessionTemplate.InitialPrompt = ""
		err := k8sClient.Create(ctx, spawner)
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "error: %v", err)
	})

	It("requires a named workspace reference", func() {
		spawner := validSessionSpawner(namespace, "missing-workspace")
		spawner.Spec.SessionTemplate.Worker.WorkspaceRef.Name = ""
		err := k8sClient.Create(ctx, spawner)
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "error: %v", err)
	})

	It("accepts credentials without session template credentials", func() {
		spawner := validSessionSpawner(namespace, "multi-account")
		spawner.Spec.SessionTemplate.Worker.Credentials = nil
		spawner.Spec.Credentials = []kelos.SpawnerCredential{
			{Name: "account-a", Type: kelos.CredentialTypeOAuth, SecretRef: kelos.SecretReference{Name: "secret-a"}},
			{Name: "account-b", Type: kelos.CredentialTypeOAuth, SecretRef: kelos.SecretReference{Name: "secret-b"}},
		}
		Expect(k8sClient.Create(ctx, spawner)).To(Succeed())
	})

	It("rejects credentials combined with session template credentials", func() {
		spawner := validSessionSpawner(namespace, "credential-conflict")
		spawner.Spec.Credentials = []kelos.SpawnerCredential{
			{Name: "account-a", Type: kelos.CredentialTypeOAuth, SecretRef: kelos.SecretReference{Name: "secret-a"}},
		}
		err := k8sClient.Create(ctx, spawner)
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "error: %v", err)
	})

	It("requires session template or spawner credentials", func() {
		spawner := validSessionSpawner(namespace, "missing-credentials")
		spawner.Spec.SessionTemplate.Worker.Credentials = nil
		err := k8sClient.Create(ctx, spawner)
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "error: %v", err)
	})
})

func validSessionSpawner(namespace, name string) *kelos.SessionSpawner {
	return &kelos.SessionSpawner{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: kelos.SessionSpawnerSpec{
			When: kelos.SessionSpawnerWhen{GitHubWebhook: &kelos.GitHubWebhook{
				Events: []string{"issue_comment"},
				Filters: []kelos.GitHubWebhookFilter{{
					Event:       "issue_comment",
					Action:      "created",
					BodyPattern: `(?m)^/kelos pick-up[ \t]*\r?$`,
				}},
			}},
			SessionTemplate: kelos.SessionTemplate{
				SessionSpec: kelos.SessionSpec{
					Worker: kelos.WorkerSpec{
						Type:         "codex",
						Credentials:  &kelos.Credentials{Type: kelos.CredentialTypeNone},
						WorkspaceRef: &kelos.WorkspaceReference{Name: "workspace"},
					},
					InitialBranch: "kelos-task-{{.Number}}",
					InitialPrompt: "Handle issue #{{.Number}}",
				},
			},
		},
	}
}
