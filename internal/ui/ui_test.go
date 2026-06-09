package ui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.klarlabs.de/rolloffs/internal/engine"
	"go.klarlabs.de/rolloffs/internal/rollout"
	pt "go.klarlabs.de/rolloffs/pkg/target"
)

type fakeBackend struct {
	rollouts         []rollout.Rollout
	drift            []engine.DriftItem
	records          []rollout.RolloutRecord
	diff             string
	resources        []pt.Resource
	approved         string
	rejected         string
	rolledBackTarget string
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
func (f *fakeBackend) RollbackLast(_ context.Context, ref string) (rollout.Rollout, error) {
	f.rolledBackTarget = ref
	return rollout.Rollout{TargetRef: ref, Phase: rollout.PhaseRolledBack}, nil
}
func (f *fakeBackend) Approve(_ context.Context, id string, _ rollout.Identity) (rollout.Rollout, error) {
	f.approved = id
	return rollout.Rollout{ID: id, Phase: rollout.PhaseVerifying}, nil
}
func (f *fakeBackend) Reject(_ context.Context, id string, _ rollout.Identity) (rollout.Rollout, error) {
	f.rejected = id
	return rollout.Rollout{ID: id, Phase: rollout.PhaseRolledBack}, nil
}

func srv(be Backend, opts ...Option) http.Handler {
	return New(be, rollout.Identity{Kind: "human", Name: "felix"}, opts...).Handler()
}

func do(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
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
	if rr := do(h, "GET", "/ui/app.js", ""); rr.Code != 200 || !strings.Contains(rr.Body.String(), "createApp") {
		t.Errorf("app.js not served (%d)", rr.Code)
	}
	if rr := do(h, "GET", "/ui/vue.global.prod.js", ""); rr.Code != 200 {
		t.Errorf("vue runtime not served (%d)", rr.Code)
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

func TestAPI_TargetDetail(t *testing.T) {
	be := &fakeBackend{
		rollouts: []rollout.Rollout{{ID: "ro-1", TargetRef: "a/prod/api", Phase: rollout.PhasePromoted, Strategy: rollout.StrategyCanary}},
		diff:     "- old\n+ new",
		resources: []pt.Resource{
			{Kind: "Deployment", Name: "api", Namespace: "prod", Status: "ready 2/2"},
			{Kind: "Pod", Name: "api-x", Namespace: "prod", Status: "Running", Parent: "api"},
		},
		records: []rollout.RolloutRecord{{RolloutID: "ro-1", Phase: rollout.PhasePromoted, Initiator: rollout.Identity{Kind: "ci", Name: "rec"}}},
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
	if len(tj.Resources) != 2 || tj.Resources[1].Parent != "api" {
		t.Errorf("resource tree = %+v", tj.Resources)
	}
	if len(tj.History) != 1 {
		t.Errorf("history = %+v", tj.History)
	}
}

func TestAPI_TargetNotFound(t *testing.T) {
	if rr := do(srv(&fakeBackend{}), "GET", "/ui/api/target?ref=missing", ""); rr.Code != http.StatusNotFound {
		t.Errorf("missing target = %d, want 404", rr.Code)
	}
}

func TestAPI_Actions(t *testing.T) {
	be := &fakeBackend{}
	h := srv(be)
	if rr := do(h, "POST", "/ui/api/approve", `{"id":"ro-7"}`); rr.Code != 200 || be.approved != "ro-7" {
		t.Errorf("approve = %d approved=%q", rr.Code, be.approved)
	}
	if rr := do(h, "POST", "/ui/api/reject", `{"id":"ro-8"}`); rr.Code != 200 || be.rejected != "ro-8" {
		t.Errorf("reject = %d rejected=%q", rr.Code, be.rejected)
	}
	if rr := do(h, "POST", "/ui/api/rollback", `{"target":"a/prod/api"}`); rr.Code != 200 || be.rolledBackTarget != "a/prod/api" {
		t.Errorf("rollback = %d target=%q", rr.Code, be.rolledBackTarget)
	}
	if rr := do(h, "POST", "/ui/api/approve", `{}`); rr.Code != http.StatusBadRequest {
		t.Errorf("approve without id = %d, want 400", rr.Code)
	}
}

func TestAPI_Sync(t *testing.T) {
	if rr := do(srv(&fakeBackend{}), "POST", "/ui/api/sync", "{}"); rr.Code != http.StatusNotImplemented {
		t.Errorf("sync without wiring = %d, want 501", rr.Code)
	}
	called := false
	h := srv(&fakeBackend{}, WithSync(func(context.Context) error { called = true; return nil }))
	if rr := do(h, "POST", "/ui/api/sync", "{}"); rr.Code != 200 || !called {
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
