package reporting

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// TestRemoveLabel asserts the DELETE verb, the exact REST path, and that the
// label segment is URL-escaped. A label containing a space or a slash is legal
// on GitHub, and an unescaped one silently targets the wrong path.
func TestRemoveLabel(t *testing.T) {
	var (
		mu        sync.Mutex
		gotMethod string
		gotPath   string
		gotAuth   string
		calls     int
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[]`))
	}))
	defer server.Close()

	reporter := &GitHubReporter{
		Owner:   "test-owner",
		Repo:    "test-repo",
		Token:   "test-token",
		BaseURL: server.URL,
	}

	if err := reporter.RemoveLabel(context.Background(), 42, "kelos-auto"); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if calls != 1 {
		t.Errorf("Expected 1 API call, got %d", calls)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("Expected DELETE, got %s", gotMethod)
	}
	if gotPath != "/repos/test-owner/test-repo/issues/42/labels/kelos-auto" {
		t.Errorf("Unexpected path: %s", gotPath)
	}
	if gotAuth != "token test-token" {
		t.Errorf("Expected auth %q, got %q", "token test-token", gotAuth)
	}
}

func TestRemoveLabelEscapesLabelName(t *testing.T) {
	var gotRawPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// EscapedPath reports the wire form, which is what proves the escape
		// happened; r.URL.Path is already decoded.
		gotRawPath = r.URL.EscapedPath()
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[]`))
	}))
	defer server.Close()

	reporter := &GitHubReporter{Owner: "o", Repo: "r", Token: "t", BaseURL: server.URL}

	if err := reporter.RemoveLabel(context.Background(), 7, "needs triage/urgent"); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	want := "/repos/o/r/issues/7/labels/needs%20triage%2Furgent"
	if gotRawPath != want {
		t.Errorf("Expected escaped path %q, got %q", want, gotRawPath)
	}
}

// TestRemoveLabelNotFoundIsSuccess is the branch that actually runs in
// production: the label is already gone (a previous attempt landed, or a human
// removed it), and GitHub answers 404. The desired end state holds, so the call
// must report success — otherwise every re-report logs a spurious failure and a
// caller that treats the error as fatal blocks on a no-op.
func TestRemoveLabelNotFoundIsSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"message":"Label does not exist"}`))
	}))
	defer server.Close()

	reporter := &GitHubReporter{Owner: "o", Repo: "r", Token: "t", BaseURL: server.URL}

	if err := reporter.RemoveLabel(context.Background(), 42, "kelos-auto"); err != nil {
		t.Fatalf("Expected 404 to be treated as success, got error: %v", err)
	}
}

// TestRemoveLabelIsIdempotent calls RemoveLabel twice against a server that
// removes on the first call and 404s on the second, which is exactly what
// GitHub does. Both calls must succeed.
func TestRemoveLabelIsIdempotent(t *testing.T) {
	var (
		mu    sync.Mutex
		calls int
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[]`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"message":"Label does not exist"}`))
	}))
	defer server.Close()

	reporter := &GitHubReporter{Owner: "o", Repo: "r", Token: "t", BaseURL: server.URL}

	for i := 1; i <= 2; i++ {
		if err := reporter.RemoveLabel(context.Background(), 42, "kelos-auto"); err != nil {
			t.Fatalf("Call %d: unexpected error: %v", i, err)
		}
	}
	if calls != 2 {
		t.Errorf("Expected 2 API calls, got %d", calls)
	}
}

// TestRemoveLabelError proves a real failure is still reported. 404 is the only
// status folded into success; a 403 (missing Issues:write) must not be silently
// swallowed, or the leak this method exists to close becomes invisible.
func TestRemoveLabelError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"message":"Resource not accessible by personal access token"}`))
	}))
	defer server.Close()

	reporter := &GitHubReporter{Owner: "o", Repo: "r", Token: "t", BaseURL: server.URL}

	err := reporter.RemoveLabel(context.Background(), 42, "kelos-auto")
	if err == nil {
		t.Fatal("Expected an error for status 403, got nil")
	}
}

func TestAddLabels(t *testing.T) {
	var (
		mu        sync.Mutex
		gotMethod string
		gotPath   string
		gotBody   struct {
			Labels []string `json:"labels"`
		}
		calls int
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		gotMethod = r.Method
		gotPath = r.URL.Path
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[]`))
	}))
	defer server.Close()

	reporter := &GitHubReporter{Owner: "o", Repo: "r", Token: "t", BaseURL: server.URL}

	if err := reporter.AddLabels(context.Background(), 42, []string{"kelos-attempt-1"}); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if calls != 1 {
		t.Errorf("Expected 1 API call, got %d", calls)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("Expected POST, got %s", gotMethod)
	}
	if gotPath != "/repos/o/r/issues/42/labels" {
		t.Errorf("Unexpected path: %s", gotPath)
	}
	if len(gotBody.Labels) != 1 || gotBody.Labels[0] != "kelos-attempt-1" {
		t.Errorf("Unexpected labels payload: %v", gotBody.Labels)
	}
}

// TestAddLabelsEmptyIsNoOp keeps the caller free of a length check: adding
// nothing must not spend an API call.
func TestAddLabelsEmptyIsNoOp(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	reporter := &GitHubReporter{Owner: "o", Repo: "r", Token: "t", BaseURL: server.URL}

	if err := reporter.AddLabels(context.Background(), 42, nil); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if calls != 0 {
		t.Errorf("Expected 0 API calls for an empty label list, got %d", calls)
	}
}

func TestListLabels(t *testing.T) {
	var gotPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode([]labelResponse{
			{Name: "kelos-auto"},
			{Name: "kelos-attempt-2"},
		})
	}))
	defer server.Close()

	reporter := &GitHubReporter{Owner: "o", Repo: "r", Token: "t", BaseURL: server.URL}

	labels, err := reporter.ListLabels(context.Background(), 42)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if gotPath != "/repos/o/r/issues/42/labels" {
		t.Errorf("Unexpected path: %s", gotPath)
	}
	if len(labels) != 2 || labels[0] != "kelos-auto" || labels[1] != "kelos-attempt-2" {
		t.Errorf("Unexpected labels: %v", labels)
	}
}

func TestListLabelsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"message":"nope"}`))
	}))
	defer server.Close()

	reporter := &GitHubReporter{Owner: "o", Repo: "r", Token: "t", BaseURL: server.URL}

	if _, err := reporter.ListLabels(context.Background(), 42); err == nil {
		t.Fatal("Expected an error for status 403, got nil")
	}
}
