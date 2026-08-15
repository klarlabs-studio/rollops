package grpcapi

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"go.klarlabs.de/rollops/internal/api"
	"go.klarlabs.de/rollops/internal/config"
	"go.klarlabs.de/rollops/internal/engine"
	"go.klarlabs.de/rollops/internal/grpcapi/rollopsv1"
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

func dialBuf(t *testing.T) rollopsv1.RolloutServiceClient {
	t.Helper()
	return dialBufWithID(t, func() string { return "ro-grpc" })
}

func dialBufWithID(t *testing.T, idgen func() string) rollopsv1.RolloutServiceClient {
	t.Helper()
	db, err := sqlite.Open(t.TempDir() + "/g.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
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
	auth := api.TokenAuth{"t-felix": {Kind: "human", Name: "felix"}, "t-bot": {Kind: "ci", Name: "bot"}}

	lis := bufconn.Listen(1 << 20)
	gs := NewGRPCServer(New(eng, auth, pol))
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return rollopsv1.NewRolloutServiceClient(conn)
}

func withToken(token string) context.Context {
	return metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token))
}

func TestGRPC_Unauthenticated(t *testing.T) {
	c := dialBuf(t)
	_, err := c.Plan(context.Background(), &rollopsv1.PlanRequest{Config: cfgYAML})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("anonymous plan code = %v, want Unauthenticated", status.Code(err))
	}
}

func TestGRPC_PlanApplyStatus(t *testing.T) {
	c := dialBuf(t)
	ctx := withToken("t-felix")

	p, err := c.Plan(ctx, &rollopsv1.PlanRequest{Config: cfgYAML})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if p.GetSummary() == "" {
		t.Error("empty plan summary")
	}
	a, err := c.Apply(ctx, &rollopsv1.ApplyRequest{Config: cfgYAML})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if a.GetId() != "ro-grpc" || a.GetPhase() != "verifying" {
		t.Errorf("apply = %+v", a)
	}
	s, err := c.Status(ctx, &rollopsv1.StatusRequest{Id: "ro-grpc"})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if s.GetPhase() != "verifying" {
		t.Errorf("status = %+v", s)
	}
	if s.GetStepIndex() != 4 || s.GetStepTotal() != 4 || s.GetStepWeight() != 100 {
		t.Errorf("step progress = %d/%d (%d%%), want 4/4 (100%%)", s.GetStepIndex(), s.GetStepTotal(), s.GetStepWeight())
	}
}

func TestGRPC_RollbackLast(t *testing.T) {
	n := 0
	c := dialBufWithID(t, func() string {
		n++
		return "ro-grpc-" + string(rune('0'+n))
	})
	ctx := withToken("t-felix")

	if _, err := c.Apply(ctx, &rollopsv1.ApplyRequest{Config: cfgYAML}); err != nil {
		t.Fatalf("Apply first: %v", err)
	}
	cfg2 := strings.Replace(cfgYAML, "x: 1", "x: 2", 1)
	if _, err := c.Apply(ctx, &rollopsv1.ApplyRequest{Config: cfg2}); err != nil {
		t.Fatalf("Apply second: %v", err)
	}
	rb, err := c.Rollback(ctx, &rollopsv1.RollbackRequest{Target: "demo/prod/app"})
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if rb.GetPhase() != "rolled-back" || rb.GetTarget() != "demo/prod/app" {
		t.Errorf("rollback = %+v", rb)
	}
}

func TestGRPC_Promote(t *testing.T) {
	c := dialBuf(t)
	ctx := withToken("t-felix")
	if _, err := c.Apply(ctx, &rollopsv1.ApplyRequest{Config: cfgYAML}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	pr, err := c.Promote(ctx, &rollopsv1.RolloutActionRequest{Id: "ro-grpc"})
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if pr.GetPhase() != "promoted" || pr.GetTarget() != "demo/prod/app" {
		t.Errorf("promote = %+v", pr)
	}
}

func TestGRPC_Approve_NotAwaiting(t *testing.T) {
	// A verifying (not awaiting-approval) rollout can't be approved — proves the
	// Approve RPC routes to the engine and surfaces the precondition error.
	c := dialBuf(t)
	ctx := withToken("t-felix")
	if _, err := c.Apply(ctx, &rollopsv1.ApplyRequest{Config: cfgYAML}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := c.Approve(ctx, &rollopsv1.RolloutActionRequest{Id: "ro-grpc"}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("approve of non-awaiting rollout code = %v, want FailedPrecondition", status.Code(err))
	}
}

func TestGRPC_RBACDeniesViewerApply(t *testing.T) {
	c := dialBuf(t)
	_, err := c.Apply(withToken("t-bot"), &rollopsv1.ApplyRequest{Config: cfgYAML})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("viewer apply code = %v, want PermissionDenied", status.Code(err))
	}
}

func TestGRPC_FleetStatus(t *testing.T) {
	c := dialBuf(t)
	ctx := withToken("t-felix")
	if _, err := c.Apply(ctx, &rollopsv1.ApplyRequest{Config: cfgYAML}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	rep, err := c.FleetStatus(ctx, &rollopsv1.FleetStatusRequest{Filter: "demo/prod/app"})
	if err != nil {
		t.Fatalf("FleetStatus: %v", err)
	}
	if rep.GetTotal() != 1 || rep.GetName() != "demo/prod/app" {
		t.Fatalf("fleet = %+v", rep)
	}
	if _, err := c.FleetStatus(ctx, &rollopsv1.FleetStatusRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("empty filter code = %v", status.Code(err))
	}
	// Viewer may read fleet (status perm).
	if _, err := c.FleetStatus(withToken("t-bot"), &rollopsv1.FleetStatusRequest{Filter: "demo/prod/app"}); err != nil {
		t.Fatalf("viewer fleet: %v", err)
	}
}

func TestGRPC_RBACDeniesViewerRollback(t *testing.T) {
	c := dialBuf(t)
	_, err := c.Rollback(withToken("t-bot"), &rollopsv1.RollbackRequest{Target: "demo/prod/app"})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("viewer rollback code = %v, want PermissionDenied", status.Code(err))
	}
}
