package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.klarlabs.de/rollops/internal/api"
	"go.klarlabs.de/rollops/internal/config"
	"go.klarlabs.de/rollops/internal/engine"
	"go.klarlabs.de/rollops/internal/git"
	"go.klarlabs.de/rollops/internal/security"
	"go.klarlabs.de/rollops/internal/store/sqlite"
	itarget "go.klarlabs.de/rollops/internal/target"
	pt "go.klarlabs.de/rollops/pkg/target"
)

// daemonTopMux mirrors cmd/rollopsd: the GitHub hook sits on the top mux so HMAC
// is the auth (no bearer), while the rest of /v1 still requires a token.
func daemonTopMux(t *testing.T, secret string, tick func(context.Context, string)) http.Handler {
	t.Helper()
	db, err := sqlite.Open(t.TempDir() + "/d.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := itarget.NewRegistry()
	reg.Register("fake", func(config.Target) (pt.Target, error) { return stubTarget{}, nil })
	eng := engine.New(db, reg)
	auth := api.TokenAuth{"tok": {Kind: "human", Name: "admin"}}
	pol := security.DefaultRBACPolicy()
	top := http.NewServeMux()
	attachGitHubWebhook(top, secret, tick)
	top.Handle("/", api.New(eng, auth, pol).Handler())
	return top
}

type stubTarget struct{}

func (stubTarget) Apply(context.Context, pt.Manifest) (pt.Result, error) {
	return pt.Result{}, nil
}
func (stubTarget) Observe(context.Context) (pt.Fingerprint, error) { return pt.Fingerprint{}, nil }
func (stubTarget) Health(context.Context) (pt.HealthStatus, error) {
	return pt.HealthStatus{State: pt.HealthHealthy}, nil
}

func TestGitHubWebhook_ValidPOSTCallsTick(t *testing.T) {
	secret := "webhook-secret"
	body := `{"ref":"refs/heads/main","repository":{"full_name":"acme/web"}}`
	var ticks int
	var hint string
	h := daemonTopMux(t, secret, func(_ context.Context, repo string) {
		ticks++
		hint = repo
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/hooks/github", strings.NewReader(body))
	req.Header.Set(git.SignatureHeader, git.Sign([]byte(secret), []byte(body)))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("valid POST status = %d, want 202; body=%s", rr.Code, rr.Body.Bytes())
	}
	if ticks != 1 {
		t.Fatalf("Tick calls = %d, want 1", ticks)
	}
	if hint != "acme/web" {
		t.Errorf("repo hint = %q, want acme/web", hint)
	}
}

func TestGitHubWebhook_InvalidSignatureDoesNotTick(t *testing.T) {
	secret := "webhook-secret"
	body := `{"repository":{"full_name":"acme/web"}}`
	var ticks int
	h := daemonTopMux(t, secret, func(context.Context, string) { ticks++ })

	req := httptest.NewRequest(http.MethodPost, "/v1/hooks/github", strings.NewReader(body))
	req.Header.Set(git.SignatureHeader, git.Sign([]byte(secret), []byte("nope")))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("invalid signature status = %d, want 401", rr.Code)
	}
	if ticks != 0 {
		t.Fatalf("Tick calls = %d, want 0", ticks)
	}
}

func TestGitHubWebhook_UnsetSecretIs404(t *testing.T) {
	var ticks int
	h := daemonTopMux(t, "", func(context.Context, string) { ticks++ })
	body := `{"repository":{"full_name":"acme/web"}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/hooks/github", strings.NewReader(body))
	req.Header.Set(git.SignatureHeader, git.Sign([]byte("anything"), []byte(body)))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("unset secret status = %d, want 404 (must not be an open tick)", rr.Code)
	}
	if ticks != 0 {
		t.Fatalf("Tick calls = %d, want 0", ticks)
	}
}

func TestGitHubWebhook_DoesNotRequireBearer(t *testing.T) {
	// The hook is HMAC-auth, not bearer. A POST without Authorization must not
	// fall through to the REST API's 401.
	secret := "webhook-secret"
	body := `{"repository":{"full_name":"acme/web"}}`
	h := daemonTopMux(t, secret, func(context.Context, string) {})
	req := httptest.NewRequest(http.MethodPost, "/v1/hooks/github", strings.NewReader(body))
	req.Header.Set(git.SignatureHeader, git.Sign([]byte(secret), []byte(body)))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code == http.StatusUnauthorized {
		t.Fatal("webhook POST must not require a bearer token")
	}
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rr.Code)
	}
	plan := httptest.NewRequest(http.MethodPost, "/v1/plan", strings.NewReader("{}"))
	pr := httptest.NewRecorder()
	h.ServeHTTP(pr, plan)
	if pr.Code != http.StatusUnauthorized {
		t.Fatalf("/v1/plan without bearer = %d, want 401", pr.Code)
	}
	_, _ = io.Copy(io.Discard, pr.Result().Body)
}
