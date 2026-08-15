package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.klarlabs.de/rollops/internal/api"
	"go.klarlabs.de/rollops/internal/engine"
	"go.klarlabs.de/rollops/internal/rollout"
	"go.klarlabs.de/rollops/internal/security"
	"go.klarlabs.de/rollops/internal/servertls"
	"go.klarlabs.de/rollops/internal/ui"
	pt "go.klarlabs.de/rollops/pkg/target"
)

func TestIsLoopbackAddr(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:8080", true},
		{"localhost:8080", true},
		{"[::1]:8080", true},
		{":8080", false}, // bare port binds all interfaces
		{"0.0.0.0:8080", false},
		{"192.168.1.10:8080", false},
		{"example.com:8080", false},
		{"127.0.0.1", true}, // no port
	}
	for _, c := range cases {
		if got := isLoopbackAddr(c.addr); got != c.want {
			t.Errorf("isLoopbackAddr(%q) = %v, want %v", c.addr, got, c.want)
		}
	}
}

func TestEnsureTransportSecure(t *testing.T) {
	// Loopback is always allowed, TLS or not (same-host proxy / in-pod mesh hop).
	if err := ensureTransportSecure("127.0.0.1:8080", "HTTP", nil); err != nil {
		t.Errorf("loopback plaintext should be allowed: %v", err)
	}
	// Non-loopback without TLS fails closed — no override.
	if err := ensureTransportSecure(":8080", "HTTP", nil); err == nil {
		t.Error("non-loopback without TLS must be refused")
	}
	// Non-loopback WITH TLS configured is allowed.
	tlsCfg := &servertls.Config{}
	if err := ensureTransportSecure(":8080", "HTTP", tlsCfg); err != nil {
		t.Errorf("non-loopback with TLS should be allowed: %v", err)
	}
}

// stubBackend is a minimal ui.Backend for wiring tests: it records the actor
// that approved so the test can prove the REAL principal (not a static admin)
// reached the engine.
type stubBackend struct {
	rollout    rollout.Rollout
	approvedBy rollout.Identity
}

func (b *stubBackend) List(context.Context, int) ([]rollout.Rollout, error) {
	return []rollout.Rollout{b.rollout}, nil
}
func (b *stubBackend) DriftReport(context.Context) ([]engine.DriftItem, error) { return nil, nil }
func (b *stubBackend) History(context.Context, string) ([]rollout.RolloutRecord, error) {
	return nil, nil
}
func (b *stubBackend) Diff(context.Context, string) (string, error)             { return "", nil }
func (b *stubBackend) Resources(context.Context, string) ([]pt.Resource, error) { return nil, nil }
func (b *stubBackend) Approve(_ context.Context, id string, by rollout.Identity) (rollout.Rollout, error) {
	b.approvedBy = by
	return rollout.Rollout{ID: id, Phase: rollout.PhaseVerifying}, nil
}
func (b *stubBackend) Reject(_ context.Context, id string, _ rollout.Identity) (rollout.Rollout, error) {
	return rollout.Rollout{ID: id}, nil
}
func (b *stubBackend) Promote(_ context.Context, id string, _ rollout.Identity, _ bool) (rollout.Rollout, error) {
	return rollout.Rollout{ID: id}, nil
}
func (b *stubBackend) Pause(_ context.Context, id string, _ rollout.Identity) (rollout.Rollout, error) {
	return rollout.Rollout{ID: id}, nil
}
func (b *stubBackend) Resume(_ context.Context, id string, _ rollout.Identity) (rollout.Rollout, error) {
	return rollout.Rollout{ID: id}, nil
}
func (b *stubBackend) Abort(_ context.Context, id string, _ rollout.Identity) (rollout.Rollout, error) {
	return rollout.Rollout{ID: id}, nil
}
func (b *stubBackend) RollbackLast(_ context.Context, ref string, _ bool) (rollout.Rollout, error) {
	return rollout.Rollout{TargetRef: ref}, nil
}
func (b *stubBackend) Freeze(_ context.Context, on bool, _ rollout.Identity, reason string) (bool, string, error) {
	return on, reason, nil
}
func (b *stubBackend) FreezeStatus() (bool, string) { return false, "" }

// TestUIAuth_InjectsRealIdentityForRBAC proves the daemon wiring: uiAuth puts the
// bearer-authenticated identity into the request context, and the console
// authorizes and attributes the action to that real principal — not a static
// admin.
func TestUIAuth_InjectsRealIdentityForRBAC(t *testing.T) {
	t.Setenv("ROLLOPS_UI_PASSWORD", "") // force the OIDC/bearer path only

	be := &stubBackend{rollout: rollout.Rollout{ID: "ro-1", TargetRef: "a/prod/api"}}
	pol := security.NewPolicy()
	pol.DefineRole(security.Role{Name: "op", Grants: []security.Grant{{Perm: security.PermApprove}}})
	pol.Bind("human:ada", "op")

	console := ui.New(be, rollout.Identity{Kind: "human", Name: "static-admin"}, ui.WithPolicy(pol)).Handler()
	bearer := api.TokenAuth{"ada-token": rollout.Identity{Kind: "human", Name: "ada"}}
	h := uiAuth(console, bearer)

	// Authenticated as ada (who holds PermApprove) → 200, attributed to ada.
	req := httptest.NewRequest("POST", "/ui/api/approve", strings.NewReader(`{"id":"ro-1"}`))
	req.Header.Set("Authorization", "Bearer ada-token")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("approve as ada = %d, want 200 (body %s)", rr.Code, rr.Body.String())
	}
	if be.approvedBy.Name != "ada" {
		t.Errorf("approve attributed to %q, want the real principal ada (static-admin leak)", be.approvedBy.Name)
	}
}
