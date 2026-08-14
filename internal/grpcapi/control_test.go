package grpcapi

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
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

func dialBufCanary(t *testing.T) rollopsv1.RolloutServiceClient {
	t.Helper()
	db, err := sqlite.Open(t.TempDir() + "/g.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := itarget.NewRegistry()
	reg.Register("fake", func(config.Target) (pt.Target, error) { return fakeTarget{}, nil })
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	eng := engine.New(db, reg, engine.WithClock(func() time.Time { return now }), engine.WithIDGen(func() string { return "ro-grpc" }))

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

func TestGRPC_PauseResumeAbort(t *testing.T) {
	c := dialBufCanary(t)
	ctx := withToken("t-felix")
	if _, err := c.Apply(ctx, &rollopsv1.ApplyRequest{Config: canaryPauseYAML}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	type rpc func(context.Context, *rollopsv1.RolloutActionRequest, ...grpc.CallOption) (*rollopsv1.RolloutActionResponse, error)
	for _, tc := range []struct {
		name  string
		call  rpc
		phase string
	}{
		{"pause", c.Pause, "paused"},
		{"resume", c.Resume, "deploying"},
		{"abort", c.Abort, "rolled-back"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.call(withToken("t-bot"), &rollopsv1.RolloutActionRequest{Id: "ro-grpc"}); status.Code(err) != codes.PermissionDenied {
				t.Fatalf("viewer %s code = %v, want PermissionDenied", tc.name, status.Code(err))
			}
			out, err := tc.call(ctx, &rollopsv1.RolloutActionRequest{Id: "ro-grpc"})
			if err != nil {
				t.Fatalf("operator %s: %v", tc.name, err)
			}
			if out.GetPhase() != tc.phase {
				t.Errorf("operator %s phase = %q, want %q", tc.name, out.GetPhase(), tc.phase)
			}
		})
	}
}
