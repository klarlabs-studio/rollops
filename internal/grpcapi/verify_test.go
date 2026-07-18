package grpcapi

import (
	"context"
	"testing"

	"go.klarlabs.de/rollops/internal/config"
	"go.klarlabs.de/rollops/internal/engine"
)

// TestClient_VerifyRoundTripThroughGRPC proves the dry-run report survives the
// wire: every gate, its status, and the overall verdict.
func TestClient_VerifyRoundTripThroughGRPC(t *testing.T) {
	rpc := dialBuf(t)
	c := &Client{rpc: rpc, token: "t-felix"}
	ctx := context.Background()

	cfg, err := config.Load([]byte(cfgYAML))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Apply(ctx, engine.ApplyRequest{Config: cfg}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	rep, err := c.Verify(ctx, "ro-grpc")
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !rep.OK {
		t.Errorf("healthy target should verify over gRPC: %+v", rep.Gates)
	}
	if rep.RolloutID != "ro-grpc" {
		t.Errorf("rollout id = %q, want ro-grpc", rep.RolloutID)
	}
	if rep.Phase != "verifying" {
		t.Errorf("phase = %q, want verifying (reported, not changed)", rep.Phase)
	}
	if len(rep.Gates) != 3 {
		t.Fatalf("got %d gates over the wire, want all 3: %+v", len(rep.Gates), rep.Gates)
	}
	for _, g := range rep.Gates {
		if g.Gate == "" || g.Status == "" {
			t.Errorf("gate lost fields over the wire: %+v", g)
		}
	}

	// The dry run changed nothing: the rollout is still awaiting promotion.
	s, err := c.Status(ctx, "ro-grpc")
	if err != nil {
		t.Fatal(err)
	}
	if string(s.Phase) != "verifying" {
		t.Errorf("phase after a dry run = %q, want it untouched", s.Phase)
	}
}

// TestClient_VerifyRBACPropagates proves the dry run is gated on promote
// permission, not read permission — it runs real gates on the daemon host.
func TestClient_VerifyRBACPropagates(t *testing.T) {
	rpc := dialBuf(t)
	ctx := context.Background()
	cfg, _ := config.Load([]byte(cfgYAML))
	if _, err := (&Client{rpc: rpc, token: "t-felix"}).Apply(ctx, engine.ApplyRequest{Config: cfg}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	viewer := &Client{rpc: rpc, token: "t-bot"}
	if _, err := viewer.Verify(ctx, "ro-grpc"); err == nil {
		t.Fatal("viewer verify must be denied through the gRPC client")
	}
}
