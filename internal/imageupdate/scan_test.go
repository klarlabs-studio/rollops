package imageupdate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseRef(t *testing.T) {
	cases := []struct{ in, host, repo string }{
		{"ghcr.io/acme/app:v1", "ghcr.io", "acme/app"},
		{"ghcr.io/acme/app", "ghcr.io", "acme/app"},
		{"docker.io/library/nginx:1.27", "docker.io", "library/nginx"},
		{"nginx", "docker.io", "library/nginx"},
		{"registry:5000/team/svc:tag", "registry:5000", "team/svc"},
	}
	for _, c := range cases {
		h, r := ParseRef(c.in)
		if h != c.host || r != c.repo {
			t.Errorf("ParseRef(%q) = %q,%q want %q,%q", c.in, h, r, c.host, c.repo)
		}
	}
}

func TestScanner_Tags_WithBearerChallenge(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/token"):
			// Verify the scope was carried and (private) basic creds sent.
			if u, p, _ := r.BasicAuth(); u != "me" || p != "pat" {
				t.Errorf("token basic auth = %q/%q", u, p)
			}
			w.Write([]byte(`{"token":"BEARER123"}`))
		case strings.HasSuffix(r.URL.Path, "/tags/list"):
			if r.Header.Get("Authorization") != "Bearer BEARER123" {
				// First (unauth) hit: issue the challenge.
				w.Header().Set("WWW-Authenticate", `Bearer realm="`+srv.URL+`/token",service="reg",scope="repository:acme/app:pull"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Write([]byte(`{"name":"acme/app","tags":["v1.0.0","v1.1.0","v1.2.0"]}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	host := strings.TrimPrefix(srv.URL, "http://")
	sc := Scanner{HTTP: srv.Client(), Username: "me", Password: "pat"}
	// Point the scanner at the test server by using its host in the ref. The
	// scanner builds https://<host>/v2/...; rewrite to the test transport.
	sc.HTTP = rewriteClient(srv)
	tags, err := sc.Tags(context.Background(), host+"/acme/app:v1.0.0")
	if err != nil {
		t.Fatalf("Tags: %v", err)
	}
	if len(tags) != 3 || tags[2] != "v1.2.0" {
		t.Errorf("tags = %v", tags)
	}
}

// rewriteClient sends every request to the httptest server regardless of host,
// so the scanner's https URLs reach the test transport.
func rewriteClient(srv *httptest.Server) *http.Client {
	base := srv.Client().Transport
	return &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		r.URL.Scheme = "http"
		r.URL.Host = strings.TrimPrefix(srv.URL, "http://")
		return base.RoundTrip(r)
	})}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSelectTag(t *testing.T) {
	tags := []string{"v1.0.0", "v1.1.0", "v1.2.3", "v2.0.0", "v2.1.0-rc1", "latest"}

	if got, ok := SelectTag("v1.0.0", tags, "minor", ""); !ok || got != "v1.2.3" {
		t.Errorf("minor: got %q ok=%v, want v1.2.3", got, ok)
	}
	if got, ok := SelectTag("v1.0.0", tags, "major", ""); !ok || got != "v2.0.0" {
		t.Errorf("major: got %q ok=%v, want v2.0.0", got, ok)
	}
	if got, ok := SelectTag("v1.2.0", tags, "patch", ""); !ok || got != "v1.2.3" {
		t.Errorf("patch: got %q ok=%v, want v1.2.3", got, ok)
	}
	// Already at the highest minor → no update.
	if _, ok := SelectTag("v1.2.3", tags, "minor", ""); ok {
		t.Error("no newer minor should report no update")
	}
	// Pre-release (v2.1.0-rc1) is never selected.
	if got, _ := SelectTag("v2.0.0", tags, "major", ""); got == "v2.1.0-rc1" {
		t.Error("pre-release tag must not be selected")
	}
	// Non-semver current → no selection.
	if _, ok := SelectTag("latest", tags, "major", ""); ok {
		t.Error("non-semver current must not select")
	}
}
