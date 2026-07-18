package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.klarlabs.de/rollops/internal/config"
	"go.klarlabs.de/rollops/internal/engine"
	"go.klarlabs.de/rollops/internal/security"
	"go.klarlabs.de/rollops/internal/store/sqlite"
	itarget "go.klarlabs.de/rollops/internal/target"
	pt "go.klarlabs.de/rollops/pkg/target"
)

// degradingTarget reports healthy until it is flipped, so a deploy can succeed
// (the deploy path has its own health gate) and the POST-deploy gate can then
// see an unhealthy target.
type degradingTarget struct{ unhealthy atomic.Bool }

func (d *degradingTarget) Apply(context.Context, pt.Manifest) (pt.Result, error) {
	return pt.Result{Changed: true}, nil
}
func (d *degradingTarget) Observe(context.Context) (pt.Fingerprint, error) {
	return pt.Fingerprint{}, nil
}
func (d *degradingTarget) Health(context.Context) (pt.HealthStatus, error) {
	if d.unhealthy.Load() {
		return pt.HealthStatus{State: pt.HealthUnhealthy, Reason: "503"}, nil
	}
	return pt.HealthStatus{State: pt.HealthHealthy}, nil
}

// newServerUnhealthyAfterDeploy builds a server whose target goes unhealthy the
// moment the deploy finishes. Mirrors newServerWithAuthAndID, differing only in
// the registered target.
func newServerUnhealthyAfterDeploy(t *testing.T) http.Handler {
	t.Helper()
	db, err := sqlite.Open(t.TempDir() + "/a.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	tgt := &degradingTarget{}
	reg := itarget.NewRegistry()
	reg.Register("fake", func(config.Target) (pt.Target, error) { return tgt, nil })
	tick := 0
	eng := engine.New(db, reg, engine.WithClock(func() time.Time {
		tick++
		return time.Unix(int64(tick), 0)
	}), engine.WithIDGen(func() string { return "ro-api" }))

	pol := security.NewPolicy()
	pol.DefineRole(security.Role{Name: "op", Grants: []security.Grant{
		{Perm: security.PermPlan}, {Perm: security.PermApply}, {Perm: security.PermStatus},
		{Perm: security.PermRollback}, {Perm: security.PermPromote}, {Perm: security.PermApprove},
	}})
	pol.Bind("human:felix", "op")
	auth := TokenAuth{"tok-felix": {Kind: "human", Name: "felix"}}

	h := New(eng, auth, pol).Handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r)
		if strings.HasSuffix(r.URL.Path, "/apply") {
			tgt.unhealthy.Store(true) // degrade once the deploy is done
		}
	})
}

// verifyBody is the decoded /v1/verify report.
type verifyBody struct {
	RolloutID string `json:"rolloutId"`
	TargetRef string `json:"targetRef"`
	Phase     string `json:"phase"`
	OK        bool   `json:"ok"`
	Reason    string `json:"reason"`
	Gates     []struct {
		Gate   string `json:"gate"`
		Status string `json:"status"`
		Detail string `json:"detail"`
	} `json:"gates"`
}

func TestAPI_VerifyReportsGatesAndChangesNothing(t *testing.T) {
	h := newServer(t)
	if rr := do(h, "POST", "/v1/apply", "tok-felix", cfgYAML); rr.Code != http.StatusAccepted {
		t.Fatalf("apply = %d: %s", rr.Code, rr.Body)
	}
	before := do(h, "GET", "/v1/rollouts/ro-api", "tok-felix", "")

	rr := do(h, "POST", "/v1/verify", "tok-felix", `{"id":"ro-api"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("verify = %d: %s", rr.Code, rr.Body)
	}
	var got verifyBody
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode report: %v (%s)", err, rr.Body)
	}
	if !got.OK {
		t.Errorf("healthy target should verify: %+v", got.Gates)
	}
	if got.RolloutID != "ro-api" {
		t.Errorf("rolloutId = %q, want ro-api", got.RolloutID)
	}
	if len(got.Gates) != 3 {
		t.Errorf("got %d gates, want all 3 reported: %+v", len(got.Gates), got.Gates)
	}

	// The dry run must leave the rollout exactly as it was.
	after := do(h, "GET", "/v1/rollouts/ro-api", "tok-felix", "")
	if before.Body.String() != after.Body.String() {
		t.Errorf("status changed across a dry run:\nbefore %s\nafter  %s", before.Body, after.Body)
	}
}

// TestAPI_VerifyRequiresPromotePermission proves the dry run is not a read
// operation: the gates really run, so a viewer may not trigger it.
func TestAPI_VerifyRequiresPromotePermission(t *testing.T) {
	h := newServer(t)
	if rr := do(h, "POST", "/v1/apply", "tok-felix", cfgYAML); rr.Code != http.StatusAccepted {
		t.Fatalf("apply = %d: %s", rr.Code, rr.Body)
	}
	// tok-bot is a viewer: it may read status...
	if rr := do(h, "GET", "/v1/rollouts/ro-api", "tok-bot", ""); rr.Code != http.StatusOK {
		t.Fatalf("viewer status = %d, want 200", rr.Code)
	}
	// ...but must not run gates that execute commands on the daemon host.
	if rr := do(h, "POST", "/v1/verify", "tok-bot", `{"id":"ro-api"}`); rr.Code != http.StatusForbidden {
		t.Errorf("viewer verify = %d, want 403", rr.Code)
	}
}

func TestAPI_VerifyRejectsAnonymousAndMissingID(t *testing.T) {
	h := newServer(t)
	if rr := do(h, "POST", "/v1/verify", "", `{"id":"ro-api"}`); rr.Code != http.StatusUnauthorized {
		t.Errorf("anonymous verify = %d, want 401", rr.Code)
	}
	if rr := do(h, "POST", "/v1/verify", "tok-felix", `{}`); rr.Code != http.StatusBadRequest {
		t.Errorf("verify without id = %d, want 400", rr.Code)
	}
	if rr := do(h, "POST", "/v1/verify", "tok-felix", `{"id":"nope"}`); rr.Code != http.StatusNotFound {
		t.Errorf("verify of an unknown rollout = %d, want 404", rr.Code)
	}
}

// TestAPI_VerifyFailingGateIsNotAnHTTPError proves a failed gate is a 200 with
// ok=false — an operator asking "would this promote?" gets an answer, not an
// error status.
func TestAPI_VerifyFailingGateIsNotAnHTTPError(t *testing.T) {
	h := newServerUnhealthyAfterDeploy(t)
	if rr := do(h, "POST", "/v1/apply", "tok-felix", cfgYAML); rr.Code != http.StatusAccepted {
		t.Fatalf("apply = %d: %s", rr.Code, rr.Body)
	}
	rr := do(h, "POST", "/v1/verify", "tok-felix", `{"id":"ro-api"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("a failing gate should still be 200, got %d: %s", rr.Code, rr.Body)
	}
	var got verifyBody
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.OK {
		t.Error("unhealthy target should report ok=false")
	}
	if !strings.Contains(got.Reason, "health") {
		t.Errorf("reason = %q, want the health failure", got.Reason)
	}
}
