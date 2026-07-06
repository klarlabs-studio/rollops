package kubernetes

import (
	"strings"
	"testing"

	"go.klarlabs.de/rollops/internal/config"
	"go.klarlabs.de/rollops/internal/security"
)

func confineFromEnv(m map[string]string) security.Confinement {
	return security.ConfinementFromEnv(func(k string) string { return m[k] })
}

// TestNewKubectl_NamespaceAllowlist covers namespace confinement: an in-scope
// namespace builds, an out-of-scope one is rejected before any kubectl call.
func TestNewKubectl_NamespaceAllowlist(t *testing.T) {
	conf := confineFromEnv(map[string]string{"ROLLOPS_ALLOWED_NAMESPACES": "tenant-a"})

	if _, err := newKubectl(spec{"resource": "deployment/api", "namespace": "tenant-a"}, "t/a", conf); err != nil {
		t.Errorf("in-scope namespace must build, got %v", err)
	}

	_, err := newKubectl(spec{"resource": "deployment/api", "namespace": "tenant-b"}, "t/b", conf)
	if err == nil {
		t.Fatal("out-of-scope namespace must be rejected")
	}
	if !strings.Contains(err.Error(), "not allow-listed") {
		t.Errorf("error should name the allowlist, got %v", err)
	}
}

// TestNewTarget_NamespaceAllowlist confirms the rejection surfaces as a target
// build error (→ per-target / rollout failure) via the factory seam.
func TestNewTarget_NamespaceAllowlist(t *testing.T) {
	conf := confineFromEnv(map[string]string{"ROLLOPS_ALLOWED_NAMESPACES": "tenant-a"})
	cfg := config.Target{Ref: "t/b", Spec: map[string]any{"resource": "deployment/api", "namespace": "tenant-b"}}
	if _, err := newTarget(cfg, conf); err == nil {
		t.Fatal("newTarget must fail for an out-of-scope namespace")
	}
}

// TestNewKubectl_ClusterConfinement covers dropping repo kubeconfig/context when
// cluster confinement is on, and preserving them (multi-cluster) when it is off.
func TestNewKubectl_ClusterConfinement(t *testing.T) {
	repoSpec := spec{
		"resource":   "deployment/api",
		"namespace":  "web",
		"kubeconfig": "/host/creds/prod.kubeconfig",
		"context":    "prod",
	}

	t.Run("confined drops kubeconfig/context", func(t *testing.T) {
		conf := confineFromEnv(map[string]string{"ROLLOPS_CONFINE_TARGET_CLUSTER": "1"})
		cc, err := newKubectl(repoSpec, "t/a", conf)
		if err != nil {
			t.Fatalf("newKubectl: %v", err)
		}
		got := strings.Join(cc.(*kubectlCluster).baseArgs(), " ")
		if want := "-n web"; got != want {
			t.Errorf("confined baseArgs = %q, want %q (no --kubeconfig/--context)", got, want)
		}
	})

	t.Run("unconfined preserves multi-cluster values", func(t *testing.T) {
		cc, err := newKubectl(repoSpec, "t/a", security.Confinement{})
		if err != nil {
			t.Fatalf("newKubectl: %v", err)
		}
		got := strings.Join(cc.(*kubectlCluster).baseArgs(), " ")
		want := "--kubeconfig /host/creds/prod.kubeconfig --context prod -n web"
		if got != want {
			t.Errorf("unconfined baseArgs = %q, want %q", got, want)
		}
	})
}

// TestNewTarget_DefaultsUnchanged verifies a zero-value confinement leaves
// today's behavior intact (any namespace, repo kubeconfig/context honoured).
func TestNewTarget_DefaultsUnchanged(t *testing.T) {
	cfg := config.Target{Ref: "t/a", Spec: map[string]any{
		"resource": "deployment/api", "namespace": "anything", "kubeconfig": "/k", "context": "c",
	}}
	if _, err := newTarget(cfg, security.Confinement{}); err != nil {
		t.Errorf("default (off) confinement must build any target, got %v", err)
	}
}
