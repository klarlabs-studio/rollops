package imageupdate

import (
	"context"
	"crypto/tls"
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
	// An https registry whose token realm is on the SAME host: creds must be sent.
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/token"):
			// Verify the scope was carried and (private) basic creds sent.
			if u, p, _ := r.BasicAuth(); u != "me" || p != "pat" {
				t.Errorf("token basic auth = %q/%q", u, p)
			}
			_, _ = w.Write([]byte(`{"token":"BEARER123"}`))
		case strings.HasSuffix(r.URL.Path, "/tags/list"):
			if r.Header.Get("Authorization") != "Bearer BEARER123" {
				// First (unauth) hit: issue the challenge, realm on the same host.
				w.Header().Set("WWW-Authenticate", `Bearer realm="`+srv.URL+`/token",service="reg",scope="repository:acme/app:pull"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"name":"acme/app","tags":["v1.0.0","v1.1.0","v1.2.0"]}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	// The scanner builds https://<host>/v2/…; using the TLS server's host in the
	// ref makes those requests reach it directly (srv.Client trusts the cert).
	host := strings.TrimPrefix(srv.URL, "https://")
	sc := Scanner{HTTP: srv.Client(), Username: "me", Password: "pat"}
	tags, err := sc.Tags(context.Background(), host+"/acme/app:v1.0.0")
	if err != nil {
		t.Fatalf("Tags: %v", err)
	}
	if len(tags) != 3 || tags[2] != "v1.2.0" {
		t.Errorf("tags = %v", tags)
	}
}

// TestScanner_Token_RealmHostMismatch is the core of finding #3: when the
// bearer challenge points the token realm at a DIFFERENT host than the image's
// registry, the configured basic creds (a registry PAT) must NOT be attached —
// otherwise a repo naming `image: attacker.example/foo` exfiltrates the global
// credential to a host of its choosing.
func TestScanner_Token_RealmHostMismatch(t *testing.T) {
	var attackerGotCreds bool
	// The "attacker" auth host records whether it received Authorization.
	attacker := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := r.BasicAuth(); ok {
			attackerGotCreds = true
		}
		_, _ = w.Write([]byte(`{"token":"LEAKED"}`))
	}))
	defer attacker.Close()

	reg := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/tags/list") {
			// Point the realm at the attacker's host (cross-host).
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+attacker.URL+`/token",service="reg",scope="repository:acme/app:pull"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(404)
	}))
	defer reg.Close()

	// One client that reaches both TLS test servers (test-only insecure transport).
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	host := strings.TrimPrefix(reg.URL, "https://")
	sc := Scanner{HTTP: client, Username: "me", Password: "pat"}
	_, _ = sc.Tags(context.Background(), host+"/acme/app:v1.0.0")
	if attackerGotCreds {
		t.Fatal("registry PAT was leaked to a cross-host token realm")
	}
}

// TestScanner_Token_RealmSchemeHTTP verifies creds are withheld from a realm on
// the correct host but served over cleartext http.
func TestScanner_Token_RealmSchemeHTTP(t *testing.T) {
	if !realmTrusted("https://ghcr.io/token", "ghcr.io") {
		t.Error("same-host https realm should be trusted")
	}
	if realmTrusted("http://ghcr.io/token", "ghcr.io") {
		t.Error("cleartext http realm must not be trusted")
	}
	if realmTrusted("https://evil.example/token", "ghcr.io") {
		t.Error("cross-host realm must not be trusted")
	}
	// Docker Hub's first-party auth host is trusted for docker.io.
	if !realmTrusted("https://auth.docker.io/token", "docker.io") {
		t.Error("docker.io first-party auth host should be trusted")
	}
	if realmTrusted("https://auth.docker.io.evil.example/token", "docker.io") {
		t.Error("look-alike auth host must not be trusted")
	}
}

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

func TestSemverTagsDesc(t *testing.T) {
	in := []string{"v1.0.0", "v2.0.0", "v1.2.3", "v2.1.0-rc1", "latest", "abc123"}
	got := SemverTagsDesc(in, "")
	want := []string{"v2.0.0", "v1.2.3", "v1.0.0"} // sorted desc; pre-release + non-semver dropped
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("SemverTagsDesc = %v, want %v", got, want)
	}
	// Pattern filter applies.
	if got := SemverTagsDesc(in, `^v1\.`); strings.Join(got, ",") != "v1.2.3,v1.0.0" {
		t.Errorf("pattern-filtered = %v", got)
	}
	if !IsSemver("v1.2.3") || IsSemver("v2.1.0-rc1") || IsSemver("latest") {
		t.Error("IsSemver classification wrong")
	}
}

// Mirrors the real registry: images tagged with commit SHAs + latest (no
// semver). Semver selection must ignore them gracefully — never panic, never
// pick a SHA — so SHA-tagged apps fall back to digest mode instead.
func TestSelectTag_CommitSHATagsIgnored(t *testing.T) {
	shaTags := []string{
		"1a21311a2a2ec6655e9a242c46df2007ab5f1adc",
		"30e868c372b800ce84f38c44072b8e43179f11b4",
		"latest",
	}
	// SHA-tagged current → no semver to compare → no selection (no crash).
	if got, ok := SelectTag("1a21311a2a2ec6655e9a242c46df2007ab5f1adc", shaTags, "minor", ""); ok {
		t.Errorf("SHA current must not select, got %q", got)
	}
	// Semver current, only SHA/latest candidates → nothing qualifies.
	if got, ok := SelectTag("v1.0.0", shaTags, "minor", ""); ok {
		t.Errorf("SHA/latest candidates must not be selected, got %q", got)
	}
	// A SHA candidate alongside real semver: the semver wins, SHA ignored.
	mixed := append([]string{"v1.1.0"}, shaTags...)
	if got, ok := SelectTag("v1.0.0", mixed, "minor", ""); !ok || got != "v1.1.0" {
		t.Errorf("semver must win over SHA tags, got %q ok=%v", got, ok)
	}
}
