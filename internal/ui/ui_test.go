package ui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/rollops/internal/engine"
	"go.klarlabs.de/rollops/internal/rollout"
	"go.klarlabs.de/rollops/internal/security"
	pt "go.klarlabs.de/rollops/pkg/target"
)

type fakeBackend struct {
	rollouts         []rollout.Rollout
	drift            []engine.DriftItem
	records          []rollout.RolloutRecord
	diff             string
	resources        []pt.Resource
	approved         string
	approvedBy       rollout.Identity
	rejected         string
	promoted         string
	rolledBackTarget string
	frozen           bool
	frozenBy         rollout.Identity
	freezeReason     string
}

func (f *fakeBackend) List(context.Context, int) ([]rollout.Rollout, error) { return f.rollouts, nil }
func (f *fakeBackend) DriftReport(context.Context) ([]engine.DriftItem, error) {
	return f.drift, nil
}
func (f *fakeBackend) History(context.Context, string) ([]rollout.RolloutRecord, error) {
	return f.records, nil
}
func (f *fakeBackend) Diff(context.Context, string) (string, error) { return f.diff, nil }
func (f *fakeBackend) Resources(context.Context, string) ([]pt.Resource, error) {
	return f.resources, nil
}
func (f *fakeBackend) RollbackLast(_ context.Context, ref string, _ bool) (rollout.Rollout, error) {
	f.rolledBackTarget = ref
	return rollout.Rollout{TargetRef: ref, Phase: rollout.PhaseRolledBack}, nil
}
func (f *fakeBackend) Approve(_ context.Context, id string, by rollout.Identity) (rollout.Rollout, error) {
	f.approved, f.approvedBy = id, by
	return rollout.Rollout{ID: id, Phase: rollout.PhaseVerifying}, nil
}
func (f *fakeBackend) Reject(_ context.Context, id string, _ rollout.Identity) (rollout.Rollout, error) {
	f.rejected = id
	return rollout.Rollout{ID: id, Phase: rollout.PhaseRolledBack}, nil
}
func (f *fakeBackend) Promote(_ context.Context, id string, _ rollout.Identity, _ bool) (rollout.Rollout, error) {
	f.promoted = id
	return rollout.Rollout{ID: id, Phase: rollout.PhasePromoted}, nil
}
func (f *fakeBackend) Freeze(_ context.Context, on bool, by rollout.Identity, reason string) (bool, string, error) {
	f.frozen, f.frozenBy, f.freezeReason = on, by, reason
	if !on {
		f.freezeReason = ""
	}
	return f.frozen, f.freezeReason, nil
}
func (f *fakeBackend) FreezeStatus() (bool, string) { return f.frozen, f.freezeReason }

// privileged has every mutating permission; viewer has none. testPolicy binds
// both so RBAC enforcement can be exercised end-to-end.
var (
	privileged = rollout.Identity{Kind: "human", Name: "felix"}
	viewer     = rollout.Identity{Kind: "human", Name: "nobody"}
)

func testPolicy() *security.Policy {
	p := security.NewPolicy()
	p.DefineRole(security.Role{Name: "op", Grants: []security.Grant{
		{Perm: security.PermApprove}, {Perm: security.PermPromote}, {Perm: security.PermRollback},
		{Perm: security.PermFreeze}, {Perm: security.PermApply}, {Perm: security.PermStatus},
	}})
	p.DefineRole(security.Role{Name: "viewer", Grants: []security.Grant{{Perm: security.PermStatus}}})
	p.Bind("human:felix", "op")
	p.Bind("human:nobody", "viewer")
	return p
}

func srv(be Backend, opts ...Option) http.Handler {
	return New(be, rollout.Identity{Kind: "human", Name: "admin"}, opts...).Handler()
}

// do issues a request with no authenticated identity in context (read-only
// endpoints ignore it; mutating ones must reject it).
func do(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	return doAsCtx(h, method, path, body, context.Background())
}

// doAs issues a request as the given authenticated identity.
func doAs(h http.Handler, id rollout.Identity, path, body string) *httptest.ResponseRecorder {
	return doAsCtx(h, "POST", path, body, WithIdentity(context.Background(), id))
}

func doAsCtx(h http.Handler, method, path, body string, ctx context.Context) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestSPA_ServesIndexAndAssets(t *testing.T) {
	h := srv(&fakeBackend{})
	if rr := do(h, "GET", "/ui", ""); rr.Code != 200 || !strings.Contains(rr.Body.String(), `id="app"`) {
		t.Fatalf("index = %d, body lacks app root", rr.Code)
	}
	if rr := do(h, "GET", "/ui", ""); rr.Code != 200 || !strings.Contains(rr.Body.String(), ".filter") {
		t.Fatalf("index = %d, body lacks dashboard filter styling", rr.Code)
	}
	if rr := do(h, "GET", "/ui", ""); rr.Code != 200 || !strings.Contains(rr.Body.String(), `rel="icon"`) {
		t.Fatalf("index = %d, body lacks embedded favicon", rr.Code)
	}
	// app.js is the esbuild bundle (Vue + Zod + app). Identifiers are minified,
	// so assert on a stable template literal that survives minification.
	if rr := do(h, "GET", "/ui/app.js", ""); rr.Code != 200 || !strings.Contains(rr.Body.String(), "Filter targets") {
		t.Errorf("app.js bundle not served (%d)", rr.Code)
	}
	if rr := do(h, "GET", "/ui/app.js", ""); rr.Code != 200 || !strings.Contains(rr.Body.String(), "Attention") {
		t.Errorf("app.js bundle lacks attention queue (%d)", rr.Code)
	}
	if rr := do(h, "GET", "/ui/app.js", ""); rr.Code != 200 || !strings.Contains(rr.Body.String(), "Operational risk") {
		t.Errorf("app.js bundle lacks Argo-like application list (%d)", rr.Code)
	}
	if rr := do(h, "GET", "/ui/app.js", ""); rr.Code != 200 || !strings.Contains(rr.Body.String(), "Desired from Git") {
		t.Errorf("app.js bundle lacks desired/live/runtime detail split (%d)", rr.Code)
	}
	if rr := do(h, "GET", "/ui/app.js", ""); rr.Code != 200 || !strings.Contains(rr.Body.String(), "Timeline") {
		t.Errorf("app.js bundle lacks rollout timeline (%d)", rr.Code)
	}
	if rr := do(h, "GET", "/ui/app.js", ""); rr.Code != 200 || strings.Contains(rr.Body.String(), "decisionkit") {
		t.Error("app.js must not label the blast-radius score as decisionkit")
	}
	if rr := do(h, "GET", "/ui/app.js", ""); rr.Code != 200 || !strings.Contains(rr.Body.String(), "blast-radius") {
		t.Error("app.js bundle lacks blast-radius risk label")
	}
}

func TestSPA_SecurityHeaders(t *testing.T) {
	rr := do(srv(&fakeBackend{}), "GET", "/ui", "")
	if csp := rr.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("CSP = %q", csp)
	}
	if rr.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("missing nosniff: %q", rr.Header().Get("X-Content-Type-Options"))
	}
}

func TestAPI_Dashboard(t *testing.T) {
	be := &fakeBackend{
		rollouts: []rollout.Rollout{{ID: "ro-1", TargetRef: "a/prod/api", Phase: rollout.PhasePromoted, Strategy: rollout.StrategyCanary, Initiator: rollout.Identity{Kind: "ci", Name: "rec"}}},
		drift:    []engine.DriftItem{{TargetRef: "a/prod/api", Phase: rollout.PhasePromoted, Desired: "abc", Observed: "abc", Drifted: false}},
	}
	rr := do(srv(be), "GET", "/ui/api/dashboard", "")
	var d dashboardJSON
	if err := json.Unmarshal(rr.Body.Bytes(), &d); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if d.Counts["promoted"] != 1 || len(d.Rollouts) != 1 || d.Rollouts[0].By != "ci/rec" {
		t.Errorf("dashboard = %+v", d)
	}
	if len(d.Drift) != 1 || d.Drift[0].Target != "a/prod/api" {
		t.Errorf("drift = %+v", d.Drift)
	}
}

func TestAPI_DashboardCarriesRiskActorAndTime(t *testing.T) {
	at := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	be := &fakeBackend{
		rollouts: []rollout.Rollout{{
			ID: "ro-1", TargetRef: "a/prod/api", Phase: rollout.PhaseAwaitingApproval,
			Strategy: rollout.StrategyBlueGreen, RiskScore: 0.81,
			Initiator: rollout.Identity{Kind: "agent", Name: "release-bot"}, UpdatedAt: at,
		}},
	}
	rr := do(srv(be), "GET", "/ui/api/dashboard", "")
	var d dashboardJSON
	if err := json.Unmarshal(rr.Body.Bytes(), &d); err != nil {
		t.Fatalf("decode: %v", err)
	}
	r := d.Rollouts[0]
	if r.Risk != 0.81 || r.ByKind != "agent" || r.At != "2026-06-10T12:00:00Z" {
		t.Errorf("rollout = %+v", r)
	}
}

func TestAPI_TargetDetail(t *testing.T) {
	be := &fakeBackend{
		rollouts: []rollout.Rollout{{ID: "ro-1", TargetRef: "a/prod/api", Phase: rollout.PhasePromoted, Strategy: rollout.StrategyCanary}},
		drift:    []engine.DriftItem{{TargetRef: "a/prod/api", Drifted: true}},
		diff:     "- old\n+ new",
		resources: []pt.Resource{
			{Kind: "Deployment", Name: "api", Namespace: "prod", Status: "ready 2/2"},
			{Kind: "Pod", Name: "api-x", Namespace: "prod", Status: "Running", Parent: "api"},
		},
		records: []rollout.RolloutRecord{{RolloutID: "ro-1", Phase: rollout.PhasePromoted, Note: "analysis passed: 2 measurement(s)", Initiator: rollout.Identity{Kind: "ci", Name: "rec"}}},
	}
	rr := do(srv(be), "GET", "/ui/api/target?ref=a/prod/api", "")
	if rr.Code != 200 {
		t.Fatalf("target = %d", rr.Code)
	}
	var tj targetJSON
	if err := json.Unmarshal(rr.Body.Bytes(), &tj); err != nil {
		t.Fatal(err)
	}
	if tj.Rollout.Phase != "promoted" || tj.Diff != "- old\n+ new" {
		t.Errorf("detail = %+v", tj)
	}
	if !tj.Drifted {
		t.Errorf("sync status should reflect drift report, got drifted=%v", tj.Drifted)
	}
	if len(tj.Resources) != 2 || tj.Resources[1].Parent != "api" {
		t.Errorf("resource tree = %+v", tj.Resources)
	}
	if len(tj.History) != 1 {
		t.Errorf("history = %+v", tj.History)
	}
	if tj.History[0].Note == "" {
		t.Errorf("history note was not surfaced: %+v", tj.History[0])
	}
}

func TestAPI_TargetNotFound(t *testing.T) {
	if rr := do(srv(&fakeBackend{}), "GET", "/ui/api/target?ref=missing", ""); rr.Code != http.StatusNotFound {
		t.Errorf("missing target = %d, want 404", rr.Code)
	}
}

func actionsBackend() *fakeBackend {
	return &fakeBackend{rollouts: []rollout.Rollout{
		{ID: "ro-7", TargetRef: "a/prod/api"},
		{ID: "ro-8", TargetRef: "a/prod/api"},
		{ID: "ro-9", TargetRef: "a/prod/api"},
	}}
}

func TestAPI_Actions(t *testing.T) {
	be := actionsBackend()
	h := srv(be, WithPolicy(testPolicy()))
	if rr := doAs(h, privileged, "/ui/api/approve", `{"id":"ro-7"}`); rr.Code != 200 || be.approved != "ro-7" {
		t.Errorf("approve = %d approved=%q", rr.Code, be.approved)
	}
	// The action is attributed to the real principal, not a static "admin".
	if be.approvedBy.Kind != privileged.Kind || be.approvedBy.Name != privileged.Name {
		t.Errorf("approve actor = %+v, want the authenticated identity %+v", be.approvedBy, privileged)
	}
	if rr := doAs(h, privileged, "/ui/api/reject", `{"id":"ro-8"}`); rr.Code != 200 || be.rejected != "ro-8" {
		t.Errorf("reject = %d rejected=%q", rr.Code, be.rejected)
	}
	if rr := doAs(h, privileged, "/ui/api/promote", `{"id":"ro-9"}`); rr.Code != 200 || be.promoted != "ro-9" {
		t.Errorf("promote = %d promoted=%q", rr.Code, be.promoted)
	}
	if rr := doAs(h, privileged, "/ui/api/freeze", `{"active":true,"reason":"incident"}`); rr.Code != 200 || !be.frozen {
		t.Errorf("freeze = %d frozen=%v", rr.Code, be.frozen)
	}
	if be.frozenBy.Kind != privileged.Kind || be.frozenBy.Name != privileged.Name {
		t.Errorf("freeze actor = %+v, want %+v", be.frozenBy, privileged)
	}
	if rr := doAs(h, privileged, "/ui/api/rollback", `{"target":"a/prod/api"}`); rr.Code != 200 || be.rolledBackTarget != "a/prod/api" {
		t.Errorf("rollback = %d target=%q", rr.Code, be.rolledBackTarget)
	}
	if rr := doAs(h, privileged, "/ui/api/approve", `{}`); rr.Code != http.StatusBadRequest {
		t.Errorf("approve without id = %d, want 400", rr.Code)
	}
}

// TestAPI_RBAC_DeniesUnprivileged proves a viewer (PermStatus only) is refused
// every mutating action, and that the backend is never touched.
func TestAPI_RBAC_DeniesUnprivileged(t *testing.T) {
	be := actionsBackend()
	h := srv(be, WithPolicy(testPolicy()))
	cases := []struct{ path, body string }{
		{"/ui/api/approve", `{"id":"ro-7"}`},
		{"/ui/api/reject", `{"id":"ro-8"}`},
		{"/ui/api/promote", `{"id":"ro-9"}`},
		{"/ui/api/freeze", `{"active":true,"reason":"x"}`},
		{"/ui/api/rollback", `{"target":"a/prod/api"}`},
		{"/ui/api/sync", `{}`},
	}
	for _, c := range cases {
		if rr := doAs(h, viewer, c.path, c.body); rr.Code != http.StatusForbidden {
			t.Errorf("%s as viewer = %d, want 403", c.path, rr.Code)
		}
	}
	if be.approved != "" || be.rejected != "" || be.promoted != "" || be.frozen || be.rolledBackTarget != "" {
		t.Errorf("unprivileged request reached the backend: %+v", be)
	}
}

// TestAPI_RBAC_RequiresIdentity proves a request with no authenticated identity
// in context is denied (401), never run as a static admin.
func TestAPI_RBAC_RequiresIdentity(t *testing.T) {
	be := actionsBackend()
	h := srv(be, WithPolicy(testPolicy()))
	for _, path := range []string{"/ui/api/approve", "/ui/api/reject", "/ui/api/promote", "/ui/api/freeze", "/ui/api/rollback", "/ui/api/sync"} {
		if rr := do(h, "POST", path, `{"id":"ro-7","target":"a/prod/api"}`); rr.Code != http.StatusUnauthorized {
			t.Errorf("%s without identity = %d, want 401", path, rr.Code)
		}
	}
}

// TestAPI_RBAC_FailsClosedWithoutPolicy proves that when no policy is wired the
// console denies mutating actions rather than acting as an unchecked admin.
func TestAPI_RBAC_FailsClosedWithoutPolicy(t *testing.T) {
	be := actionsBackend()
	h := srv(be) // no WithPolicy
	if rr := doAs(h, privileged, "/ui/api/approve", `{"id":"ro-7"}`); rr.Code != http.StatusForbidden {
		t.Errorf("approve with no policy = %d, want 403 (fail closed)", rr.Code)
	}
	if be.approved != "" {
		t.Errorf("fail-closed handler still reached the backend: approved=%q", be.approved)
	}
}

func TestAPI_Sync(t *testing.T) {
	pol := testPolicy()
	if rr := doAs(srv(&fakeBackend{}, WithPolicy(pol)), privileged, "/ui/api/sync", "{}"); rr.Code != http.StatusNotImplemented {
		t.Errorf("sync without wiring = %d, want 501", rr.Code)
	}
	called := false
	h := srv(&fakeBackend{}, WithPolicy(pol), WithSync(func(context.Context) error { called = true; return nil }))
	if rr := doAs(h, privileged, "/ui/api/sync", "{}"); rr.Code != 200 || !called {
		t.Errorf("sync = %d called=%v", rr.Code, called)
	}
	// canSync reflected in dashboard.
	rr := do(h, "GET", "/ui/api/dashboard", "")
	var d dashboardJSON
	_ = json.Unmarshal(rr.Body.Bytes(), &d)
	if !d.CanSync {
		t.Error("dashboard canSync should be true when sync wired")
	}
}
