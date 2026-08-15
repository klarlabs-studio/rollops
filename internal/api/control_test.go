package api

import (
	"net/http"
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

const canaryPauseYAML = `
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
    type: canary
    steps:
      - weight: 10
        pause: 50ms
      - weight: 100
        pause: 50ms
`

// newCanaryServer is the apply-gated control surface under a frozen clock so a
// 50ms canary bake does not drain inside Apply.
func newCanaryServer(t *testing.T) http.Handler {
	t.Helper()
	db, err := sqlite.Open(t.TempDir() + "/a.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := itarget.NewRegistry()
	reg.Register("fake", func(config.Target) (pt.Target, error) { return fakeTarget{}, nil })
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	eng := engine.New(db, reg, engine.WithClock(func() time.Time { return now }), engine.WithIDGen(func() string { return "ro-api" }))

	pol := security.NewPolicy()
	pol.DefineRole(security.Role{Name: "op", Grants: []security.Grant{
		{Perm: security.PermPlan}, {Perm: security.PermApply}, {Perm: security.PermStatus}, {Perm: security.PermRollback}, {Perm: security.PermPromote}, {Perm: security.PermApprove},
	}})
	pol.DefineRole(security.Role{Name: "viewer", Grants: []security.Grant{{Perm: security.PermStatus}}})
	pol.Bind("human:felix", "op")
	pol.Bind("ci:bot", "viewer")
	return New(eng, TokenAuth{
		"tok-felix": {Kind: "human", Name: "felix"},
		"tok-bot":   {Kind: "ci", Name: "bot"},
	}, pol).Handler()
}

func TestAPI_PauseResumeAbort(t *testing.T) {
	h := newCanaryServer(t)
	if rr := do(h, "POST", "/v1/apply", "tok-felix", canaryPauseYAML); rr.Code != http.StatusAccepted {
		t.Fatalf("apply = %d: %s", rr.Code, rr.Body)
	}
	if !strings.Contains(do(h, "GET", "/v1/rollouts/ro-api", "tok-felix", "").Body.String(), "deploying") {
		t.Fatal("canary must still be deploying after Apply")
	}

	for _, tc := range []struct {
		path   string
		viewer int
		phase  string
	}{
		{"/v1/pause", http.StatusForbidden, "paused"},
		{"/v1/resume", http.StatusForbidden, "deploying"},
		{"/v1/abort", http.StatusForbidden, "rolled-back"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			if rr := do(h, "POST", tc.path, "tok-bot", `{"id":"ro-api"}`); rr.Code != tc.viewer {
				t.Errorf("viewer %s = %d, want %d: %s", tc.path, rr.Code, tc.viewer, rr.Body)
			}
			rr := do(h, "POST", tc.path, "tok-felix", `{"id":"ro-api"}`)
			if rr.Code != http.StatusOK {
				t.Fatalf("operator %s = %d: %s", tc.path, rr.Code, rr.Body)
			}
			if !strings.Contains(rr.Body.String(), tc.phase) {
				t.Errorf("operator %s body = %s, want phase %q", tc.path, rr.Body, tc.phase)
			}
		})
	}
}
