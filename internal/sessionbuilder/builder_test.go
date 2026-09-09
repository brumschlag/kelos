package sessionbuilder

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/spawnercredentials"
)

func TestBuildRendersSessionTemplateAndOwnership(t *testing.T) {
	spawnerRef := SpawnerRef{
		Name:       "kelos-workers",
		UID:        types.UID("spawner-uid"),
		APIVersion: "kelos.dev/v1alpha2",
		Kind:       "SessionSpawner",
	}
	template := &kelos.SessionTemplate{
		SessionSpec: kelos.SessionSpec{
			Worker: kelos.WorkerSpec{
				Type: "codex",
				Credentials: &kelos.Credentials{
					Type: kelos.CredentialTypeOAuth,
				},
				WorkspaceRef: &kelos.WorkspaceReference{Name: "kelos"},
			},
			InitialBranch: "kelos-task-{{.Number}}",
			InitialPrompt: "Handle {{.Repository}}#{{.Number}} from {{.Sender}}",
			VolumeClaimTemplate: &corev1.PersistentVolumeClaimSpec{
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")},
				},
			},
		},
	}

	session, err := Build(
		"kelos-workers-issue-comment-abc123",
		"default",
		template,
		map[string]interface{}{"Repository": "kelos-dev/kelos", "Number": 1520, "Sender": "gjkim42"},
		spawnerRef,
	)
	if err != nil {
		t.Fatal(err)
	}

	if session.Spec.InitialBranch != "kelos-task-1520" {
		t.Fatalf("initialBranch = %q, want %q", session.Spec.InitialBranch, "kelos-task-1520")
	}
	if session.Spec.InitialPrompt != "Handle kelos-dev/kelos#1520 from gjkim42" {
		t.Fatalf("initialPrompt = %q", session.Spec.InitialPrompt)
	}
	if session.Labels[LabelSessionSpawner] != string(spawnerRef.UID) {
		t.Fatalf("%s label = %q", LabelSessionSpawner, session.Labels[LabelSessionSpawner])
	}
	if session.Annotations[AnnotationSessionSpawnerName] != spawnerRef.Name {
		t.Fatalf("%s annotation = %q", AnnotationSessionSpawnerName, session.Annotations[AnnotationSessionSpawnerName])
	}
	if len(session.OwnerReferences) != 1 || session.OwnerReferences[0].Kind != "SessionSpawner" {
		t.Fatalf("ownerReferences = %#v", session.OwnerReferences)
	}
	if template.InitialPrompt != "Handle {{.Repository}}#{{.Number}} from {{.Sender}}" {
		t.Fatalf("Build mutated template initialPrompt = %q", template.InitialPrompt)
	}
	storageRequest := session.Spec.VolumeClaimTemplate.Resources.Requests[corev1.ResourceStorage]
	if !storageRequest.Equal(resource.MustParse("10Gi")) {
		t.Fatalf("storage request = %s", storageRequest.String())
	}
}

func TestBuildRejectsMissingTemplateValue(t *testing.T) {
	_, err := Build(
		"session",
		"default",
		&kelos.SessionTemplate{SessionSpec: kelos.SessionSpec{InitialPrompt: "Handle {{.Number}}"}},
		map[string]interface{}{},
		SpawnerRef{},
	)
	if err == nil || !strings.Contains(err.Error(), `map has no entry for key "Number"`) {
		t.Fatalf("Build() error = %v", err)
	}
}

func TestBuildUsesUIDLabelForLongSessionSpawnerName(t *testing.T) {
	spawnerName := strings.Repeat("a", 200)
	session, err := Build(
		"session",
		"default",
		&kelos.SessionTemplate{},
		nil,
		SpawnerRef{Name: spawnerName, UID: types.UID("spawner-uid")},
	)
	if err != nil {
		t.Fatal(err)
	}
	if session.Labels[LabelSessionSpawner] != "spawner-uid" {
		t.Fatalf("%s label = %q", LabelSessionSpawner, session.Labels[LabelSessionSpawner])
	}
	if session.Annotations[AnnotationSessionSpawnerName] != spawnerName {
		t.Fatalf("%s annotation = %q", AnnotationSessionSpawnerName, session.Annotations[AnnotationSessionSpawnerName])
	}
}

func TestAssignSpawnerCredentialUsesConfiguredCredential(t *testing.T) {
	spawner := &kelos.SessionSpawner{
		Spec: kelos.SessionSpawnerSpec{Credentials: []kelos.SpawnerCredential{
			{Name: "account-b", Type: kelos.CredentialTypeOAuth, SecretRef: kelos.SecretReference{Name: "secret-b"}},
			{Name: "account-a", Type: kelos.CredentialTypeAPIKey, SecretRef: kelos.SecretReference{Name: "secret-a"}},
		}},
	}
	session := &kelos.Session{}

	if err := AssignSpawnerCredential(spawner, session); err != nil {
		t.Fatalf("AssignSpawnerCredential() error = %v", err)
	}
	if session.Spec.Worker.Credentials == nil || session.Spec.Worker.Credentials.SecretRef == nil {
		t.Fatal("Session credentials were not assigned")
	}
	selected := session.Labels[spawnercredentials.AssignmentLabel]
	wantSecrets := map[string]string{"account-a": "secret-a", "account-b": "secret-b"}
	wantSecret, ok := wantSecrets[selected]
	if !ok {
		t.Fatalf("credential label = %q, want a configured credential", selected)
	}
	if got := session.Spec.Worker.Credentials.SecretRef.Name; got != wantSecret {
		t.Errorf("SecretRef.Name = %q, want %q for %s", got, wantSecret, selected)
	}
}
