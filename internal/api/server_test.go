package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/rolloffs/internal/config"
	"go.klarlabs.de/rolloffs/internal/engine"
	"go.klarlabs.de/rolloffs/internal/security"
	"go.klarlabs.de/rolloffs/internal/store/sqlite"
	itarget "go.klarlabs.de/rolloffs/internal/target"
	pt "go.klarlabs.de/rolloffs/pkg/target"
)

const cfgYAML = `
apiVersion: rolloffs.klarlabs.de/v1
kind: RolloutConfig
metadata:
  name: demo
spec:
  target:
    kind: fake
    ref: demo/prod/app
    criticality: low
    spec:
      x: 1
  strategy:
    type: rolling
`

type fakeTarget struct{}

func (fakeTarget) Apply(context.Context, pt.Manifest) (pt.Result, error) {
	return pt.Result{Changed: true}, nil
}
func (fakeTarget) Observe(context.Context) (pt.Fingerprint, error) { return pt.Fingerprint{}, nil }
func (fakeTarget) Health(context.Context) (pt.HealthStatus, error) {
	return pt.HealthStatus{State: pt.HealthHealthy}, nil
}

func newServer(t *testing.T) http.Handler {
	t.Helper()
	db, err := sqlite.Open(t.TempDir() + "/a.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	reg := itarget.NewRegistry()
	reg.Register("fake", func(config.Target) (pt.Target, error) { return fakeTarget{}, nil })
	eng := engine.New(db, reg, engine.WithClock(func() time.Time { return time.Unix(0, 0) }), engine.WithIDGen(func() string { return "ro-api" }))

	pol := security.NewPolicy()
	pol.DefineRole(security.Role{Name: "op", Grants: []security.Grant{
		{Perm: security.PermPlan}, {Perm: security.PermApply}, {Perm: security.PermStatus},
	}})
	pol.DefineRole(security.Role{Name: "viewer", Grants: []security.Grant{{Perm: security.PermStatus}}})
	pol.Bind("human:felix", "op")
	pol.Bind("ci:bot", "viewer")

	auth := TokenAuth{
		"tok-felix": {Kind: "human", Name: "felix"},
		"tok-bot":   {Kind: "ci", Name: "bot"},
	}
	return New(eng, auth, pol).Handler()
}

func do(h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestAPI_AnonymousRejected(t *testing.T) {
	h := newServer(t)
	if rr := do(h, "POST", "/v1/plan", "", cfgYAML); rr.Code != http.StatusUnauthorized {
		t.Errorf("anonymous plan = %d, want 401", rr.Code)
	}
}

func TestAPI_PlanAndApplyAuthorized(t *testing.T) {
	h := newServer(t)
	if rr := do(h, "POST", "/v1/plan", "tok-felix", cfgYAML); rr.Code != http.StatusOK {
		t.Fatalf("plan = %d: %s", rr.Code, rr.Body)
	}
	rr := do(h, "POST", "/v1/apply", "tok-felix", cfgYAML)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("apply = %d: %s", rr.Code, rr.Body)
	}
	if !strings.Contains(rr.Body.String(), "ro-api") {
		t.Errorf("apply body = %s", rr.Body)
	}
}

func TestAPI_RBACForbidsViewerApply(t *testing.T) {
	h := newServer(t)
	if rr := do(h, "POST", "/v1/apply", "tok-bot", cfgYAML); rr.Code != http.StatusForbidden {
		t.Errorf("viewer apply = %d, want 403", rr.Code)
	}
	// but a viewer may read status
	_ = do(h, "POST", "/v1/apply", "tok-felix", cfgYAML)
	if rr := do(h, "GET", "/v1/rollouts/ro-api", "tok-bot", ""); rr.Code != http.StatusOK {
		t.Errorf("viewer status = %d, want 200: %s", rr.Code, rr.Body)
	}
}

func TestAPI_Healthz(t *testing.T) {
	h := newServer(t)
	if rr := do(h, "GET", "/healthz", "", ""); rr.Code != http.StatusOK {
		t.Errorf("healthz = %d", rr.Code)
	}
}
