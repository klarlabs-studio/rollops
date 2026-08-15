package git

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVerifySignature_RoundTrip(t *testing.T) {
	secret := []byte("webhook-secret")
	body := []byte(`{"ref":"refs/heads/main"}`)
	sig := Sign(secret, body)

	if err := VerifySignature(secret, body, sig); err != nil {
		t.Errorf("valid signature rejected: %v", err)
	}
}

func TestVerifySignature_Tampered(t *testing.T) {
	secret := []byte("webhook-secret")
	sig := Sign(secret, []byte("original"))
	if err := VerifySignature(secret, []byte("tampered"), sig); err == nil {
		t.Error("tampered body must fail verification")
	}
	if err := VerifySignature([]byte("wrong-secret"), []byte("original"), sig); err == nil {
		t.Error("wrong secret must fail verification")
	}
}

func TestVerifySignature_Malformed(t *testing.T) {
	for _, h := range []string{"", "abc", "sha1=deadbeef", "sha256=zzzz"} {
		if err := VerifySignature([]byte("s"), []byte("b"), h); err == nil {
			t.Errorf("malformed header %q should fail", h)
		}
	}
}

func TestSameRepo(t *testing.T) {
	cases := []struct {
		watch, hint string
		want        bool
	}{
		{"https://github.com/acme/web.git", "acme/web", true},
		{"https://github.com/acme/web", "https://github.com/acme/web.git", true},
		{"git@github.com:acme/web.git", "acme/web", true},
		{"ssh://git@github.com/acme/web", "https://github.com/acme/web", true},
		{"https://github.com/Acme/Web.git", "acme/web", true},
		{"https://github.com/acme/web.git", "acme/other", false},
		{"https://github.com/acme/web.git", "", false},
		{"", "acme/web", false},
	}
	for _, tc := range cases {
		if got := SameRepo(tc.watch, tc.hint); got != tc.want {
			t.Errorf("SameRepo(%q, %q) = %v, want %v", tc.watch, tc.hint, got, tc.want)
		}
	}
}

func TestGitHubRepoHint(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{
			name: "full_name",
			body: `{"repository":{"full_name":"acme/web","clone_url":"https://github.com/acme/web.git"}}`,
			want: "acme/web",
		},
		{
			name: "clone_url fallback",
			body: `{"repository":{"clone_url":"https://github.com/acme/web.git"}}`,
			want: "https://github.com/acme/web.git",
		},
		{
			name: "unrelated payload",
			body: `{"zen":"keep it logically awesome"}`,
			want: "",
		},
		{
			name: "not json",
			body: `not-json`,
			want: "",
		},
	}
	for _, tc := range cases {
		if got := GitHubRepoHint([]byte(tc.body)); got != tc.want {
			t.Errorf("%s: GitHubRepoHint = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestWebhookHandler_Table(t *testing.T) {
	secret := "webhook-secret"
	body := []byte(`{"ref":"refs/heads/main","repository":{"full_name":"acme/web"}}`)

	cases := []struct {
		name       string
		secret     string
		method     string
		sig        string
		wantStatus int
		wantTick   bool
		wantHint   string
	}{
		{
			name:       "unset secret is 404 and does not tick",
			secret:     "",
			method:     http.MethodPost,
			sig:        Sign([]byte(secret), body),
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "valid signature ticks matching repo",
			secret:     secret,
			method:     http.MethodPost,
			sig:        Sign([]byte(secret), body),
			wantStatus: http.StatusAccepted,
			wantTick:   true,
			wantHint:   "acme/web",
		},
		{
			name:       "invalid signature is 401 and does not tick",
			secret:     secret,
			method:     http.MethodPost,
			sig:        Sign([]byte(secret), []byte("tampered")),
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "missing signature is 401 and does not tick",
			secret:     secret,
			method:     http.MethodPost,
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "GET is not a tick even with a secret",
			secret:     secret,
			method:     http.MethodGet,
			wantStatus: http.StatusMethodNotAllowed,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ticks int
			var hint string
			h := WebhookHandler(tc.secret, func(_ context.Context, h string) {
				ticks++
				hint = h
			})
			var r *http.Request
			if tc.method == http.MethodGet {
				r = httptest.NewRequest(tc.method, "/v1/hooks/github", nil)
			} else {
				r = httptest.NewRequest(tc.method, "/v1/hooks/github", strings.NewReader(string(body)))
			}
			if tc.sig != "" {
				r.Header.Set(SignatureHeader, tc.sig)
			}
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, r)
			if rr.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d; body=%s", rr.Code, tc.wantStatus, rr.Body.Bytes())
			}
			if tc.wantTick {
				if ticks != 1 {
					t.Errorf("ticks = %d, want 1", ticks)
				}
				if hint != tc.wantHint {
					t.Errorf("hint = %q, want %q", hint, tc.wantHint)
				}
			} else if ticks != 0 {
				t.Errorf("ticks = %d, want 0", ticks)
			}
			_, _ = io.Copy(io.Discard, rr.Result().Body)
		})
	}
}
