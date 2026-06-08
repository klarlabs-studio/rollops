package ui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"go.klarlabs.de/rolloffs/internal/rollout"
)

// fakeBackend is an in-memory store of rollouts.
type fakeBackend struct {
	rollouts []rollout.Rollout
	approved string
	rejected string
}

func (f *fakeBackend) List(context.Context, int) ([]rollout.Rollout, error) {
	return f.rollouts, nil
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

func TestRejectAction_RequiresID(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/ui/reject", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	srv(&fakeBackend{}).ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("reject without id = %d, want 400", rr.Code)
	}
}
