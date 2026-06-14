package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/rollops/internal/config"
	"go.klarlabs.de/rollops/internal/engine"
	"go.klarlabs.de/rollops/internal/security"
	"go.klarlabs.de/rollops/internal/store/sqlite"
	itarget "go.klarlabs.de/rollops/internal/target"
	pt "go.klarlabs.de/rollops/pkg/target"
)

const cfgYAML = `
apiVersion: rollops.klarlabs.de/v1
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
	return newServerWithID(t, func() string { return "ro-api" })
}

func newServerWithID(t *testing.T, idgen func() string) http.Handler {
	t.Helper()
	return newServerWithAuthAndID(t, idgen, TokenAuth{
		"tok-felix": {Kind: "human", Name: "felix"},
		"tok-bot":   {Kind: "ci", Name: "bot"},
	}, nil)
}

func newServerWithAuth(t *testing.T, auth Authenticator, configure func(*security.Policy)) http.Handler {
	t.Helper()
	return newServerWithAuthAndID(t, func() string { return "ro-api" }, auth, configure)
}

func newServerWithAuthAndID(t *testing.T, idgen func() string, auth Authenticator, configure func(*security.Policy)) http.Handler {
	t.Helper()
	db, err := sqlite.Open(t.TempDir() + "/a.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	reg := itarget.NewRegistry()
	reg.Register("fake", func(config.Target) (pt.Target, error) { return fakeTarget{}, nil })
	tick := 0
	eng := engine.New(db, reg, engine.WithClock(func() time.Time {
		tick++
		return time.Unix(int64(tick), 0)
	}), engine.WithIDGen(idgen))

	pol := security.NewPolicy()
	pol.DefineRole(security.Role{Name: "op", Grants: []security.Grant{
		{Perm: security.PermPlan}, {Perm: security.PermApply}, {Perm: security.PermStatus}, {Perm: security.PermRollback}, {Perm: security.PermPromote}, {Perm: security.PermApprove},
	}})
	pol.DefineRole(security.Role{Name: "viewer", Grants: []security.Grant{{Perm: security.PermStatus}}})
	pol.Bind("human:felix", "op")
	pol.Bind("ci:bot", "viewer")
	if configure != nil {
		configure(pol)
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

func TestAPI_RollbackAuthorized(t *testing.T) {
	n := 0
	h := newServerWithID(t, func() string {
		n++
		return "ro-api-" + string(rune('0'+n))
	})
	if rr := do(h, "POST", "/v1/apply", "tok-felix", cfgYAML); rr.Code != http.StatusAccepted {
		t.Fatalf("first apply = %d: %s", rr.Code, rr.Body)
	}
	cfg2 := strings.Replace(cfgYAML, "x: 1", "x: 2", 1)
	if rr := do(h, "POST", "/v1/apply", "tok-felix", cfg2); rr.Code != http.StatusAccepted {
		t.Fatalf("second apply = %d: %s", rr.Code, rr.Body)
	}
	rr := do(h, "POST", "/v1/rollback", "tok-felix", `{"target":"demo/prod/app"}`)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("rollback = %d: %s", rr.Code, rr.Body)
	}
	if !strings.Contains(rr.Body.String(), "rolled-back") || !strings.Contains(rr.Body.String(), "demo/prod/app") {
		t.Errorf("rollback body = %s", rr.Body)
	}
}

func TestAPI_PromoteAuthorized(t *testing.T) {
	h := newServer(t)
	if rr := do(h, "POST", "/v1/apply", "tok-felix", cfgYAML); rr.Code != http.StatusAccepted {
		t.Fatalf("apply = %d: %s", rr.Code, rr.Body)
	}
	rr := do(h, "POST", "/v1/promote", "tok-felix", `{"id":"ro-api"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("promote = %d: %s", rr.Code, rr.Body)
	}
	if !strings.Contains(rr.Body.String(), "promoted") {
		t.Errorf("promote body = %s", rr.Body)
	}
}

func TestAPI_PromoteForbiddenForViewer(t *testing.T) {
	h := newServer(t)
	_ = do(h, "POST", "/v1/apply", "tok-felix", cfgYAML)
	if rr := do(h, "POST", "/v1/promote", "tok-bot", `{"id":"ro-api"}`); rr.Code != http.StatusForbidden {
		t.Errorf("viewer promote = %d, want 403", rr.Code)
	}
}

func TestAPI_FreezeForbiddenForViewer(t *testing.T) {
	// The freeze endpoint is gated by PermFreeze — a viewer (status-only) is denied
	// before reaching the engine. Proves the route + authz are wired.
	h := newServer(t)
	if rr := do(h, "POST", "/v1/freeze", "tok-bot", `{"active":true,"reason":"x"}`); rr.Code != http.StatusForbidden {
		t.Errorf("viewer freeze = %d, want 403", rr.Code)
	}
}

func TestAPI_RollbackValidationAndRBAC(t *testing.T) {
	h := newServer(t)
	if rr := do(h, "POST", "/v1/rollback", "tok-felix", `{}`); rr.Code != http.StatusBadRequest {
		t.Errorf("rollback without target = %d, want 400", rr.Code)
	}
	if rr := do(h, "POST", "/v1/rollback", "tok-bot", `{"target":"demo/prod/app"}`); rr.Code != http.StatusForbidden {
		t.Errorf("viewer rollback = %d, want 403", rr.Code)
	}
}

func TestAPI_Healthz(t *testing.T) {
	h := newServer(t)
	if rr := do(h, "GET", "/healthz", "", ""); rr.Code != http.StatusOK {
		t.Errorf("healthz = %d", rr.Code)
	}
}
