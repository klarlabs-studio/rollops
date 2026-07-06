package kubernetes

import (
	"strings"
	"testing"

	"go.klarlabs.de/rollops/internal/security"
)

func TestEvalConditions_CRDReady(t *testing.T) {
	// cert-manager Certificate: Ready=True is healthy.
	raw := `[{"type":"Ready","status":"True","reason":"Issued","message":"Certificate is up to date"}]`
	ok, reason, err := evalConditions(raw, "")
	if err != nil || !ok {
		t.Fatalf("Ready=True must be healthy, got ok=%v reason=%q err=%v", ok, reason, err)
	}
}

func TestEvalConditions_FalseIsUnhealthyWithReason(t *testing.T) {
	raw := `[{"type":"Ready","status":"False","reason":"IssuerNotReady","message":"waiting on issuer"}]`
	ok, reason, _ := evalConditions(raw, "")
	if ok {
		t.Fatal("Ready=False must be unhealthy")
	}
	if !strings.Contains(reason, "IssuerNotReady") || !strings.Contains(reason, "waiting on issuer") {
		t.Errorf("reason should carry condition reason+message, got %q", reason)
	}
}

func TestEvalConditions_UnknownIsProgressing(t *testing.T) {
	raw := `[{"type":"Available","status":"Unknown"}]`
	ok, reason, _ := evalConditions(raw, "")
	if ok || !strings.Contains(reason, "progressing") {
		t.Errorf("Unknown status should be progressing/unhealthy, got ok=%v reason=%q", ok, reason)
	}
}

func TestEvalConditions_ExplicitType(t *testing.T) {
	// An Argo Rollout-like CRD with a custom condition type.
	raw := `[{"type":"Available","status":"True"},{"type":"Healthy","status":"False","message":"degraded"}]`
	// Default picks Available (True → healthy)…
	if ok, _, _ := evalConditions(raw, ""); !ok {
		t.Error("default should pick Available=True → healthy")
	}
	// …but an explicit Healthy type gates on the failing condition.
	ok, reason, _ := evalConditions(raw, "Healthy")
	if ok || !strings.Contains(reason, "degraded") {
		t.Errorf("explicit Healthy=False should be unhealthy, got ok=%v reason=%q", ok, reason)
	}
}

func TestEvalConditions_NoConditionsIsHealthy(t *testing.T) {
	// A resource with no status.conditions (e.g. a ConfigMap) is healthy —
	// apply succeeded and there is nothing to gate on.
	if ok, _, _ := evalConditions("", ""); !ok {
		t.Error("no conditions should default to healthy")
	}
}

func TestEvalConditions_MissingRequestedType(t *testing.T) {
	raw := `[{"type":"Ready","status":"True"}]`
	if ok, reason, _ := evalConditions(raw, "Synced"); ok || !strings.Contains(reason, "Synced") {
		t.Errorf("missing explicit condition must be unhealthy naming it, got ok=%v reason=%q", ok, reason)
	}
}

func TestBaseArgs_MultiCluster(t *testing.T) {
	cc, err := newKubectl(spec{
		"kubeconfig": "/creds/prod-east.kubeconfig",
		"context":    "prod-east",
		"namespace":  "web",
		"resource":   "deployment/api",
	}, "team/prod/api", security.Confinement{})
	if err != nil {
		t.Fatalf("newKubectl: %v", err)
	}
	cl := cc.(*kubectlCluster)
	got := strings.Join(cl.baseArgs(), " ")
	want := "--kubeconfig /creds/prod-east.kubeconfig --context prod-east -n web"
	if got != want {
		t.Errorf("baseArgs = %q, want %q", got, want)
	}
}

func TestBaseArgs_AmbientWhenUnset(t *testing.T) {
	cc, err := newKubectl(spec{"resource": "deployment/api"}, "t/p/a", security.Confinement{})
	if err != nil {
		t.Fatalf("newKubectl: %v", err)
	}
	cl := cc.(*kubectlCluster)
	// No kubeconfig/context → ambient resolution; only the default namespace.
	if got := strings.Join(cl.baseArgs(), " "); got != "-n default" {
		t.Errorf("baseArgs = %q, want '-n default'", got)
	}
}

func TestResourceKind(t *testing.T) {
	cases := map[string]string{"deployment/api": "deployment", "Certificate/app": "certificate", "sts": "sts"}
	for in, want := range cases {
		if got := resourceKind(in); got != want {
			t.Errorf("resourceKind(%q) = %q, want %q", in, got, want)
		}
	}
}
