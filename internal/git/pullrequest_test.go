package git

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOwnerRepoFromURL(t *testing.T) {
	for _, tc := range []struct {
		url, owner, repo string
		wantErr          bool
	}{
		{"https://github.com/klarlabs-studio/klarlabs", "klarlabs-studio", "klarlabs", false},
		{"https://github.com/klarlabs-studio/klarlabs.git", "klarlabs-studio", "klarlabs", false},
		{"git@github.com:klarlabs-studio/klarlabs.git", "klarlabs-studio", "klarlabs", false},
		{"ssh://git@github.com/acme/web", "acme", "web", false},
		{"https://github.com/acme/web/", "acme", "web", false},
		{"https://github.com/", "", "", true},
		{"not-a-url", "", "", true},
	} {
		owner, repo, err := ownerRepoFromURL(tc.url)
		if (err != nil) != tc.wantErr {
			t.Errorf("%q: err=%v wantErr=%v", tc.url, err, tc.wantErr)
			continue
		}
		if !tc.wantErr && (owner != tc.owner || repo != tc.repo) {
			t.Errorf("%q: got %s/%s want %s/%s", tc.url, owner, repo, tc.owner, tc.repo)
		}
	}
}

// sourceFor builds a Source pointed at a stub GitHub API with a token set.
func sourceFor(t *testing.T, apiBase string) *Source {
	t.Helper()
	return Open(t.TempDir(), "main", Auth{Token: "test-token"}).
		WithURL("https://github.com/acme/web").
		WithAPIBase(apiBase)
}

func TestOpenPullRequest_CreatesAndEnablesAutoMerge(t *testing.T) {
	var createBody map[string]string
	var sawGraphQL bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("missing/wrong auth header: %q", r.Header.Get("Authorization"))
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/web/pulls":
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &createBody)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"node_id":"PR_node_1","html_url":"https://github.com/acme/web/pull/7"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/graphql":
			sawGraphQL = true
			_, _ = w.Write([]byte(`{"data":{"enablePullRequestAutoMerge":{"clientMutationId":null}}}`))
		default:
			t.Errorf("unexpected call: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	url, autoMerge, err := sourceFor(t, srv.URL).OpenPullRequest(
		context.Background(), "rollops/image/web", "main", "chore(image): bump", "body")
	if err != nil {
		t.Fatalf("OpenPullRequest: %v", err)
	}
	if url != "https://github.com/acme/web/pull/7" {
		t.Fatalf("PR url = %q", url)
	}
	if !autoMerge {
		t.Fatal("auto-merge should be enabled when the mutation succeeds")
	}
	if !sawGraphQL {
		t.Fatal("auto-merge mutation was never attempted")
	}
	if createBody["head"] != "rollops/image/web" || createBody["base"] != "main" {
		t.Fatalf("wrong PR head/base: %+v", createBody)
	}
}

// A repo without auto-merge must still get its PR opened — the feature degrades
// to "open, wait for a human", never to an error that leaves the deploy stuck.
func TestOpenPullRequest_AutoMergeDisabledStillOpens(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/graphql" {
			// GraphQL reports logical failure with 200 + an errors array.
			_, _ = w.Write([]byte(`{"errors":[{"message":"Auto-merge is not allowed for this repository"}]}`))
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"node_id":"n","html_url":"https://github.com/acme/web/pull/8"}`))
	}))
	defer srv.Close()

	url, autoMerge, err := sourceFor(t, srv.URL).OpenPullRequest(
		context.Background(), "rollops/image/web", "main", "t", "b")
	if err != nil {
		t.Fatalf("a disabled auto-merge must not be an error: %v", err)
	}
	if autoMerge {
		t.Fatal("auto-merge should report false when the repo forbids it")
	}
	if url == "" {
		t.Fatal("PR should still be opened")
	}
}

// An existing PR for the same head (GitHub answers create with 422) is reused,
// not duplicated — so re-running each poll cycle is idempotent.
func TestOpenPullRequest_ReusesExisting(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/web/pulls":
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"message":"Validation Failed","errors":[{"message":"A pull request already exists for acme:rollops/image/web."}]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/web/pulls":
			_, _ = w.Write([]byte(`[{"node_id":"existing","html_url":"https://github.com/acme/web/pull/3"}]`))
		case r.URL.Path == "/graphql":
			_, _ = w.Write([]byte(`{"data":{}}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	url, _, err := sourceFor(t, srv.URL).OpenPullRequest(
		context.Background(), "rollops/image/web", "main", "t", "b")
	if err != nil {
		t.Fatalf("reuse path errored: %v", err)
	}
	if !strings.HasSuffix(url, "/pull/3") {
		t.Fatalf("should reuse the existing PR, got %q", url)
	}
}

// A non-2xx that is not the known "already exists" 422 must surface as an error,
// not be swallowed — otherwise a real failure looks like a successful open.
func TestOpenPullRequest_HardFailureIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"Resource not accessible by integration"}`))
	}))
	defer srv.Close()

	if _, _, err := sourceFor(t, srv.URL).OpenPullRequest(
		context.Background(), "h", "main", "t", "b"); err == nil {
		t.Fatal("a 403 must be an error")
	}
}
