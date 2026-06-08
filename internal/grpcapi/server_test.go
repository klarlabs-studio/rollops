package grpcapi

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"go.klarlabs.de/rolloffs/internal/api"
	"go.klarlabs.de/rolloffs/internal/config"
	"go.klarlabs.de/rolloffs/internal/engine"
	"go.klarlabs.de/rolloffs/internal/grpcapi/rolloffsv1"
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

func dialBuf(t *testing.T) rolloffsv1.RolloutServiceClient {
	t.Helper()
	db, err := sqlite.Open(t.TempDir() + "/g.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	reg := itarget.NewRegistry()
	reg.Register("fake", func(config.Target) (pt.Target, error) { return fakeTarget{}, nil })
	eng := engine.New(db, reg, engine.WithClock(func() time.Time { return time.Unix(0, 0) }), engine.WithIDGen(func() string { return "ro-grpc" }))

	pol := security.NewPolicy()
	pol.DefineRole(security.Role{Name: "op", Grants: []security.Grant{
		{Perm: security.PermPlan}, {Perm: security.PermApply}, {Perm: security.PermStatus},
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
	t.Cleanup(func() { conn.Close() })
	return rolloffsv1.NewRolloutServiceClient(conn)
}

func withToken(token string) context.Context {
	return metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token))
}

func TestGRPC_Unauthenticated(t *testing.T) {
	c := dialBuf(t)
	_, err := c.Plan(context.Background(), &rolloffsv1.PlanRequest{Config: cfgYAML})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("anonymous plan code = %v, want Unauthenticated", status.Code(err))
	}
}

func TestGRPC_PlanApplyStatus(t *testing.T) {
	c := dialBuf(t)
	ctx := withToken("t-felix")

	p, err := c.Plan(ctx, &rolloffsv1.PlanRequest{Config: cfgYAML})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if p.GetSummary() == "" {
		t.Error("empty plan summary")
	}
	a, err := c.Apply(ctx, &rolloffsv1.ApplyRequest{Config: cfgYAML})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if a.GetId() != "ro-grpc" || a.GetPhase() != "verifying" {
		t.Errorf("apply = %+v", a)
	}
	s, err := c.Status(ctx, &rolloffsv1.StatusRequest{Id: "ro-grpc"})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if s.GetPhase() != "verifying" {
		t.Errorf("status = %+v", s)
	}
}

func TestGRPC_RBACDeniesViewerApply(t *testing.T) {
	c := dialBuf(t)
	_, err := c.Apply(withToken("t-bot"), &rolloffsv1.ApplyRequest{Config: cfgYAML})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("viewer apply code = %v, want PermissionDenied", status.Code(err))
	}
}
