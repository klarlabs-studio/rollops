package kubernetes

import (
	"context"
	"errors"
	"strings"
	"testing"

	pt "go.klarlabs.de/rollops/pkg/target"
)

// The Kubernetes target must advertise the optional preflight capability, or
// the batch check silently skips it and the protection is decorative.
func TestKubernetesTargetIsAPreflighter(t *testing.T) {
	var tgt any = &Target{}
	if _, ok := tgt.(pt.Preflighter); !ok {
		t.Fatal("*Target does not implement pt.Preflighter, so preflightBatch will skip it")
	}
}

// A refusing cluster must surface as a Preflight error rather than being
// swallowed — this is the RBAC failure that took an apex domain to 404.
func TestPreflightSurfacesTheClusterRefusal(t *testing.T) {
	want := errors.New(`middlewares.traefik.io "security-headers" is forbidden`)
	cl := &fakeCluster{preflightErr: want}
	tgt := &Target{cl: cl}

	err := tgt.Preflight(context.Background(), pt.Manifest{Kind: "kubernetes", Spec: []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\n")})
	if err == nil {
		t.Fatal("Preflight returned nil for a cluster that refused")
	}
	if !strings.Contains(err.Error(), "forbidden") {
		t.Errorf("error %q does not carry the cluster's reason", err)
	}
	if cl.preflightN != 1 {
		t.Errorf("cluster Preflight called %d times, want 1", cl.preflightN)
	}
}

// Preflight must not apply. A check with side effects is worse than none: the
// batch it exists to protect has already been half-applied by the check.
func TestPreflightAppliesNothing(t *testing.T) {
	cl := &fakeCluster{}
	tgt := &Target{cl: cl}

	if err := tgt.Preflight(context.Background(), pt.Manifest{Kind: "kubernetes", Spec: []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\n")}); err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if len(cl.applied) != 0 {
		t.Errorf("Preflight applied %d manifest(s); it must change nothing", len(cl.applied))
	}
	if cl.checksum != "" {
		t.Error("Preflight stamped a checksum; it must change nothing")
	}
}
