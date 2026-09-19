package kubernetes

import (
	"context"
	"errors"
	"strings"
	"testing"

	targetv2 "go.klarlabs.de/rollops/pkg/target/v2"
)

var probe = targetv2.DesiredState{
	Kind: "kubernetes",
	Spec: []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\n"),
}

// A refusing cluster must surface as a plan blocker rather than being
// swallowed — this is the RBAC failure that took an apex domain to 404. It is
// a blocker rather than an error because the plan succeeded: it established
// that the apply would not.
func TestPreflightSurfacesTheClusterRefusal(t *testing.T) {
	want := errors.New(`middlewares.traefik.io "security-headers" is forbidden`)
	cl := &fakeCluster{preflightErr: want}
	tgt := &Target{cl: cl}

	res, err := tgt.Plan(context.Background(), targetv2.PlanRequest{Desired: probe})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(res.Blockers) != 1 {
		t.Fatalf("a cluster that refused produced %d blockers, want 1", len(res.Blockers))
	}
	if !strings.Contains(res.Blockers[0], "forbidden") {
		t.Errorf("blocker %q does not carry the cluster's reason", res.Blockers[0])
	}
	if cl.preflightN != 1 {
		t.Errorf("cluster Preflight called %d times, want 1", cl.preflightN)
	}
}

// Planning must not apply. A check with side effects is worse than none: the
// batch it exists to protect has already been half-applied by the check.
func TestPreflightAppliesNothing(t *testing.T) {
	cl := &fakeCluster{}
	tgt := &Target{cl: cl}

	if _, err := tgt.Plan(context.Background(), targetv2.PlanRequest{Desired: probe}); err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(cl.applied) != 0 {
		t.Errorf("Preflight applied %d manifest(s); it must change nothing", len(cl.applied))
	}
	if cl.checksum != "" {
		t.Error("Plan stamped a checksum; it must change nothing")
	}
}
