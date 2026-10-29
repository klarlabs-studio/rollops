package grpcapi

import (
	"context"
	"testing"

	"go.klarlabs.de/rollops/internal/version"
)

// The daemon runs the rollouts, so a client must be able to tell which daemon
// answered it. Every response carries the daemon's version, including the
// refusals — an Unauthenticated reply is exactly when an operator wants to know
// which daemon is on the other end.
func TestClient_LearnsTheDaemonVersionFromAnyCall(t *testing.T) {
	c := dialBufClient(t, "t-felix")
	if got, ok := c.DaemonVersion(); ok {
		t.Fatalf("a client that has made no call knows a version: %q", got)
	}
	if _, err := c.Status(context.Background(), "ro-missing"); err == nil {
		t.Fatal("expected a not-found error")
	}
	got, ok := c.DaemonVersion()
	if !ok || got != version.Version {
		t.Fatalf("daemon version = %q (announced=%v), want %q", got, ok, version.Version)
	}

	bad := dialBufClient(t, "not-a-token")
	if _, err := bad.Status(context.Background(), "ro-grpc"); err == nil {
		t.Fatal("expected an unauthenticated error")
	}
	if got, ok := bad.DaemonVersion(); !ok || got != version.Version {
		t.Errorf("a refused call must still name the daemon: %q (%v)", got, ok)
	}
}
