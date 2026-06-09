package ui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"go.klarlabs.de/rolloffs/internal/engine"
	"go.klarlabs.de/rolloffs/internal/rollout"
)

// fakeBackend is an in-memory store of rollouts.
type fakeBackend struct {
	rollouts []rollout.Rollout
	drift    []engine.DriftItem
	records  []rollout.RolloutRecord
	approved string
	rejected string
}

func (f *fakeBackend) List(context.Context, int) ([]rollout.Rollout, error) {
	return f.rollouts, nil
}
func (f *fakeBackend) DriftReport(context.Context) ([]engine.DriftItem, error) {
	return f.drift, nil
}
func (f *fakeBackend) History(_ context.Context, _ string) ([]rollout.RolloutRecord, error) {
	return f.records, nil
}
func (f *fakeBackend) Approve(_ context.Context, id string, _ rollout.Identity) (rollout.Rollout, error) {
	f.approved = id
	return rollout.Rollout{ID: id, Phase: rollout.PhaseVerifying}, nil
}
func (f *fakeBackend) Reject(_ context.Context, id string, _ rollout.Identity) (rollout.Rollout, error) {
	f.rejected = id
	return rollout.Rollout{ID: id, Phase: rollout.PhaseRolledBack}, nil
}

func srv(be Backend) http.Handler {
	return New(be, rollout.Identity{Kind: "human", Name: "felix"}).Handler()
}

func TestDashboard_RendersRollouts(t *testing.T) {
	be := &fakeBackend{rollouts: []rollout.Rollout{
		{ID: "ro-1", TargetRef: "a/prod/api", Phase: rollout.PhasePromoted, Strategy: rollout.StrategyCanary},
		{ID: "ro-2", TargetRef: "b/prod/web", Phase: rollout.PhaseAwaitingApproval},
	}}
	rr := httptest.NewRecorder()
	srv(be).ServeHTTP(rr, httptest.NewRequest("GET", "/ui", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("dashboard = %d", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{"ro-1", "a/prod/api", "promoted", "ro-2", "Approve", "Reject"} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
}

func TestDashboard_OnlyAwaitingShowsActions(t *testing.T) {
	be := &fakeBackend{rollouts: []rollout.Rollout{
		{ID: "ro-done", Phase: rollout.PhasePromoted},
	}}
	rr := httptest.NewRecorder()
	srv(be).ServeHTTP(rr, httptest.NewRequest("GET", "/ui", nil))
	if strings.Contains(rr.Body.String(), "Approve") {
		t.Error("promoted rollout should not show approve action")
	}
}

func TestApproveAction(t *testing.T) {
	be := &fakeBackend{}
	form := url.Values{"id": {"ro-7"}}
	req := httptest.NewRequest("POST", "/ui/approve", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	srv(be).ServeHTTP(rr, req)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("approve = %d, want 303", rr.Code)
	}
	if be.approved != "ro-7" {
		t.Errorf("approved = %q, want ro-7", be.approved)
	}
}

func TestDashboard_ShowsDrift(t *testing.T) {
	be := &fakeBackend{
		rollouts: []rollout.Rollout{{ID: "ro-1", TargetRef: "a/prod/api", Phase: rollout.PhasePromoted}},
		drift: []engine.DriftItem{
			{TargetRef: "a/prod/api", Phase: rollout.PhasePromoted, Desired: "abc123", Observed: "stale99", Drifted: true},
		},
	}
	rr := httptest.NewRecorder()
	srv(be).ServeHTTP(rr, httptest.NewRequest("GET", "/ui", nil))
	body := rr.Body.String()
	if !strings.Contains(body, "DRIFT") || !strings.Contains(body, "drift detected") {
		t.Errorf("drift not surfaced: %s", body)
	}
	if !strings.Contains(body, "/ui/history?target=") {
		t.Error("history link missing")
	}
}

func TestHistory_RendersRecords(t *testing.T) {
	be := &fakeBackend{records: []rollout.RolloutRecord{
		{RolloutID: "ro-1", TargetRef: "a/prod/api", Phase: rollout.PhasePromoted, Initiator: rollout.Identity{Kind: "ci", Name: "rec"}},
	}}
	rr := httptest.NewRecorder()
	srv(be).ServeHTTP(rr, httptest.NewRequest("GET", "/ui/history?target=a/prod/api", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("history = %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "promoted") || !strings.Contains(rr.Body.String(), "ci/rec") {
		t.Errorf("history record not rendered: %s", rr.Body)
	}
}

func TestHistory_RequiresTarget(t *testing.T) {
	rr := httptest.NewRecorder()
	srv(&fakeBackend{}).ServeHTTP(rr, httptest.NewRequest("GET", "/ui/history", nil))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("history without target = %d, want 400", rr.Code)
	}
}

func TestSync_ButtonAndAction(t *testing.T) {
	called := false
	s := New(&fakeBackend{}, rollout.Identity{Kind: "human", Name: "x"},
		WithSync(func(context.Context) error { called = true; return nil }))
	h := s.Handler()

	// Button shown when sync is available.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/ui", nil))
	if !strings.Contains(rr.Body.String(), "Sync now") {
		t.Error("Sync now button missing when sync enabled")
	}
	// Action triggers the reconcile.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("POST", "/ui/sync", nil))
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("sync = %d, want 303", rr.Code)
	}
	if !called {
		t.Error("sync handler did not trigger reconcile")
	}
}

func TestSync_DisabledByDefault(t *testing.T) {
	h := srv(&fakeBackend{}) // no WithSync
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/ui", nil))
	if strings.Contains(rr.Body.String(), "Sync now") {
		t.Error("Sync button should be hidden when sync not wired")
	}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("POST", "/ui/sync", nil))
	if rr.Code != http.StatusNotImplemented {
		t.Errorf("sync without wiring = %d, want 501", rr.Code)
	}
}

func TestRejectAction_RequiresID(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/ui/reject", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	srv(&fakeBackend{}).ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("reject without id = %d, want 400", rr.Code)
	}
}
