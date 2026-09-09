package webhook

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

func TestFetchPRChangedFiles_FallsBackToHandlerResolver(t *testing.T) {
	// Set up a test server that requires auth and returns file list.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "token global-test-token" {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"message":"Bad credentials"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]githubFile{
			{Filename: "pkg/main.go"},
			{Filename: "README.md"},
		})
	}))
	defer srv.Close()

	// Spawner with no workspace ref — resolveGitHubTokenFromWorkspace returns "".
	spawner := &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{Name: "test-spawner", Namespace: "default"},
		Spec: kelos.TaskSpawnerSpec{
			TaskTemplate: kelos.TaskTemplate{},
		},
	}

	scheme := runtime.NewScheme()
	_ = kelos.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	cl := fake.NewClientBuilder().WithScheme(scheme).Build()

	fallbackResolver := func(context.Context) (string, error) {
		return "global-test-token", nil
	}

	files, err := fetchPRChangedFiles(context.Background(), cl, spawner, fallbackResolver, srv.URL, "org", "repo", 1)
	if err != nil {
		t.Fatalf("fetchPRChangedFiles() error = %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}
	if files[0] != "pkg/main.go" || files[1] != "README.md" {
		t.Errorf("unexpected files: %v", files)
	}
}

func TestFetchPRChangedFiles_PrefersWorkspaceToken(t *testing.T) {
	var receivedAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]githubFile{{Filename: "a.go"}})
	}))
	defer srv.Close()

	// Set up workspace with a secret containing a token.
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "ws-secret", Namespace: "default"},
		Data:       map[string][]byte{"GITHUB_TOKEN": []byte("workspace-token")},
	}
	ws := &kelos.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "test-ws", Namespace: "default"},
		Spec: kelos.WorkspaceSpec{
			SecretRef: &kelos.SecretReference{Name: "ws-secret"},
		},
	}
	spawner := &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{Name: "test-spawner", Namespace: "default"},
		Spec: kelos.TaskSpawnerSpec{
			TaskTemplate: kelos.TaskTemplate{
				WorkspaceRef: &kelos.WorkspaceReference{Name: "test-ws"},
			},
		},
	}

	scheme := runtime.NewScheme()
	_ = kelos.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret, ws).Build()

	fallbackResolver := func(context.Context) (string, error) {
		t.Fatal("fallback resolver should not be called when the workspace provides a token")
		return "", nil
	}

	_, err := fetchPRChangedFiles(context.Background(), cl, spawner, fallbackResolver, srv.URL, "org", "repo", 1)
	if err != nil {
		t.Fatalf("fetchPRChangedFiles() error = %v", err)
	}
	if receivedAuth != "token workspace-token" {
		t.Errorf("expected workspace token to be used, got Authorization: %q", receivedAuth)
	}
}

func TestFetchSessionSpawnerPRChangedFiles_PrefersWorkspaceToken(t *testing.T) {
	var receivedAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]githubFile{{Filename: "session.go"}})
	}))
	defer srv.Close()

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "ws-secret", Namespace: "default"},
		Data:       map[string][]byte{"GITHUB_TOKEN": []byte("session-workspace-token")},
	}
	workspace := &kelos.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "session-ws", Namespace: "default"},
		Spec:       kelos.WorkspaceSpec{SecretRef: &kelos.SecretReference{Name: "ws-secret"}},
	}
	spawner := &kelos.SessionSpawner{
		ObjectMeta: metav1.ObjectMeta{Name: "session-spawner", Namespace: "default"},
		Spec: kelos.SessionSpawnerSpec{SessionTemplate: kelos.SessionTemplate{SessionSpec: kelos.SessionSpec{
			Worker: kelos.WorkerSpec{WorkspaceRef: &kelos.WorkspaceReference{Name: "session-ws"}},
		}}},
	}

	scheme := runtime.NewScheme()
	_ = kelos.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret, workspace).Build()

	fallbackResolver := func(context.Context) (string, error) {
		t.Fatal("fallback resolver should not be called when the workspace provides a token")
		return "", nil
	}

	files, err := fetchSessionSpawnerPRChangedFiles(context.Background(), cl, spawner, fallbackResolver, srv.URL, "org", "repo", 1)
	if err != nil {
		t.Fatalf("fetchSessionSpawnerPRChangedFiles() error = %v", err)
	}
	if len(files) != 1 || files[0] != "session.go" {
		t.Fatalf("files = %v", files)
	}
	if receivedAuth != "token session-workspace-token" {
		t.Errorf("expected SessionSpawner workspace token, got Authorization: %q", receivedAuth)
	}
}

func TestFetchPRChangedFiles_UsesWorkerPoolWorkspaceToken(t *testing.T) {
	var receivedAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]githubFile{{Filename: "pool.go"}})
	}))
	defer srv.Close()

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "ws-secret", Namespace: "default"},
		Data:       map[string][]byte{"GITHUB_TOKEN": []byte("pool-workspace-token")},
	}
	ws := &kelos.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "pool-ws", Namespace: "default"},
		Spec: kelos.WorkspaceSpec{
			SecretRef: &kelos.SecretReference{Name: "ws-secret"},
		},
	}
	pool := &kelos.WorkerPool{
		ObjectMeta: metav1.ObjectMeta{Name: "pool", Namespace: "default"},
		Spec: kelos.WorkerPoolSpec{
			Worker: kelos.WorkerSpec{
				WorkspaceRef: &kelos.WorkspaceReference{Name: "pool-ws"},
			},
		},
	}
	spawner := &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{Name: "test-spawner", Namespace: "default"},
		Spec: kelos.TaskSpawnerSpec{
			TaskTemplate: kelos.TaskTemplate{
				WorkerPoolRef: &kelos.WorkerPoolReference{Name: "pool"},
			},
		},
	}

	scheme := runtime.NewScheme()
	_ = kelos.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret, ws, pool).Build()

	fallbackResolver := func(context.Context) (string, error) {
		t.Fatal("fallback resolver should not be called when the workspace provides a token")
		return "", nil
	}

	files, err := fetchPRChangedFiles(context.Background(), cl, spawner, fallbackResolver, srv.URL, "org", "repo", 1)
	if err != nil {
		t.Fatalf("fetchPRChangedFiles() error = %v", err)
	}
	if len(files) != 1 || files[0] != "pool.go" {
		t.Errorf("unexpected files: %v", files)
	}
	if receivedAuth != "token pool-workspace-token" {
		t.Errorf("expected pool workspace token to be used, got Authorization: %q", receivedAuth)
	}
}

func TestFetchPRChangedFiles_ResolvesGitHubAppFromWorkspace(t *testing.T) {
	// Generate a test RSA key for GitHub App auth.
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating RSA key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	})

	// Set up a test server that:
	// 1. Responds to token creation (POST /app/installations/.../access_tokens)
	// 2. Responds to the files endpoint with an auth check
	var receivedAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/app/installations/67890/access_tokens" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"token":      "ghs_app_installation_token",
				"expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
			})
			return
		}
		receivedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]githubFile{{Filename: "app.go"}})
	}))
	defer srv.Close()

	// Workspace secret with GitHub App credentials (no GITHUB_TOKEN).
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "app-secret", Namespace: "default"},
		Data: map[string][]byte{
			"appID":          []byte("12345"),
			"installationID": []byte("67890"),
			"privateKey":     keyPEM,
		},
	}
	ws := &kelos.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "app-ws", Namespace: "default"},
		Spec: kelos.WorkspaceSpec{
			SecretRef: &kelos.SecretReference{Name: "app-secret"},
		},
	}
	spawner := &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{Name: "test-spawner", Namespace: "default"},
		Spec: kelos.TaskSpawnerSpec{
			TaskTemplate: kelos.TaskTemplate{
				WorkspaceRef: &kelos.WorkspaceReference{Name: "app-ws"},
			},
		},
	}

	scheme := runtime.NewScheme()
	_ = kelos.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret, ws).Build()

	fallbackResolver := func(context.Context) (string, error) {
		t.Fatal("fallback resolver should not be called when the workspace provides GitHub App credentials")
		return "", nil
	}

	files, err := fetchPRChangedFiles(context.Background(), cl, spawner, fallbackResolver, srv.URL, "org", "repo", 1)
	if err != nil {
		t.Fatalf("fetchPRChangedFiles() error = %v", err)
	}
	if len(files) != 1 || files[0] != "app.go" {
		t.Errorf("unexpected files: %v", files)
	}
	if receivedAuth != "token ghs_app_installation_token" {
		t.Errorf("expected GitHub App installation token, got Authorization: %q", receivedAuth)
	}
}

func TestFetchPRChangedFiles_ErrorsOnInvalidWorkspaceSecret(t *testing.T) {
	// Workspace secret exists but contains no GITHUB_TOKEN and no GitHub App keys.
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "bad-secret", Namespace: "default"},
		Data:       map[string][]byte{"unrelated-key": []byte("some-value")},
	}
	ws := &kelos.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "bad-ws", Namespace: "default"},
		Spec: kelos.WorkspaceSpec{
			SecretRef: &kelos.SecretReference{Name: "bad-secret"},
		},
	}
	spawner := &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{Name: "test-spawner", Namespace: "default"},
		Spec: kelos.TaskSpawnerSpec{
			TaskTemplate: kelos.TaskTemplate{
				WorkspaceRef: &kelos.WorkspaceReference{Name: "bad-ws"},
			},
		},
	}

	scheme := runtime.NewScheme()
	_ = kelos.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret, ws).Build()

	fallbackResolver := func(context.Context) (string, error) {
		t.Fatal("fallback resolver should not be called when the workspace secret is invalid")
		return "", nil
	}

	_, err := fetchPRChangedFiles(context.Background(), cl, spawner, fallbackResolver, "http://unused", "org", "repo", 1)
	if err == nil {
		t.Fatal("expected error for workspace secret with no valid credentials, got nil")
	}
	if !strings.Contains(err.Error(), "bad-secret") {
		t.Errorf("error should reference the secret name, got: %v", err)
	}
}
