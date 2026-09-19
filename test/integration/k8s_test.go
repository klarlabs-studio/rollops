//go:build integration

package integration

import (
	"context"
	"os/exec"
	"testing"

	"go.klarlabs.de/rollops/internal/config"
	"go.klarlabs.de/rollops/internal/target/kubernetes"
	targetv2 "go.klarlabs.de/rollops/pkg/target/v2"
)

// A tiny always-ready Deployment (pause container — no real workload needed to
// exercise apply / annotate / observe / rollout-status).
const echoDeployment = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: echo
  namespace: rollops-it
spec:
  replicas: 1
  selector:
    matchLabels: {app: echo}
  template:
    metadata:
      labels: {app: echo}
    spec:
      containers:
        - name: pause
          image: registry.k8s.io/pause:3.9
`

func kubeCtx(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("kubectl"); err != nil {
		t.Skip("kubectl not available")
	}
	if out, err := exec.Command("kubectl", "config", "current-context").Output(); err != nil || len(out) == 0 {
		t.Skip("no kube context")
	}
	// Reachable cluster?
	if err := exec.Command("kubectl", "get", "ns").Run(); err != nil {
		t.Skip("cluster not reachable; start minikube")
	}
	out, _ := exec.Command("kubectl", "config", "current-context").Output()
	return string(out[:len(out)-1])
}

func TestKubernetesTarget_Live(t *testing.T) {
	ctxName := kubeCtx(t)
	// Namespace for the test (idempotent).
	_ = exec.Command("kubectl", "create", "namespace", "rollops-it").Run()
	t.Cleanup(func() { _ = exec.Command("kubectl", "delete", "namespace", "rollops-it", "--wait=false").Run() })

	cfg := config.Target{
		Kind: "kubernetes",
		Ref:  "int/k8s",
		Spec: map[string]any{
			"context":   ctxName,
			"namespace": "rollops-it",
			"resource":  "deployment/echo",
		},
	}
	tgt, err := kubernetes.New(cfg)
	if err != nil {
		t.Fatalf("new k8s target: %v", err)
	}
	ctx := context.Background()
	d := targetv2.DesiredState{Kind: "kubernetes", Spec: []byte(echoDeployment), Checksum: "k8s-live-v1"}

	// Apply: kubectl apply + annotate the live resource with the checksum and
	// the key that produced it.
	res, err := tgt.Apply(ctx, targetv2.ApplyRequest{
		Desired:        d,
		IdempotencyKey: targetv2.IdempotencyKeyFor("integration", "k8s-live-1"),
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !res.Changed {
		t.Error("first apply should report changed")
	}

	// Observe reads the checksum annotation back from the LIVE cluster (rich),
	// alongside rollout readiness.
	obs, err := tgt.Observe(ctx, targetv2.ObserveRequest{})
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if obs.Fingerprint != "k8s-live-v1" {
		t.Errorf("live cluster observed %q, want k8s-live-v1", obs.Fingerprint)
	}
	if obs.Health.State != targetv2.HealthHealthy {
		t.Logf("health = %v (%s) — acceptable if rollout still progressing", obs.Health.State, obs.Health.Reason)
	}

	// A second key over the same desired state is a new operation, not a
	// replay, so this measures convergence against a real cluster rather than
	// the annotation that records the key.
	res2, err := tgt.Apply(ctx, targetv2.ApplyRequest{
		Desired:        d,
		IdempotencyKey: targetv2.IdempotencyKeyFor("integration", "k8s-live-2"),
	})
	if err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	if res2.Changed {
		t.Error("re-applying the same checksum must be a no-op")
	}
}

const pruneNS = "rollops-prune-it"

func k8sTarget(t *testing.T) *targetv2.Bound {
	t.Helper()
	tgt, err := kubernetes.New(config.Target{
		Kind: "kubernetes", Ref: "int/prune",
		Spec: map[string]any{"context": "minikube", "namespace": pruneNS, "resource": "deployment/web", "prune": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	return tgt
}

// TestKubernetesTarget_Prune_Live proves GitOps pruning: a resource removed from
// desired is garbage-collected on the next apply.
func TestKubernetesTarget_Prune_Live(t *testing.T) {
	kubeCtx(t)
	_ = exec.Command("kubectl", "create", "namespace", pruneNS).Run()
	t.Cleanup(func() { _ = exec.Command("kubectl", "delete", "namespace", pruneNS, "--wait=false").Run() })
	ctx := context.Background()
	tgt := k8sTarget(t)

	withCM := `apiVersion: apps/v1
kind: Deployment
metadata: {name: web, namespace: ` + pruneNS + `}
spec:
  replicas: 1
  selector: {matchLabels: {app: web}}
  template:
    metadata: {labels: {app: web}}
    spec:
      containers: [{name: pause, image: registry.k8s.io/pause:3.9}]
---
apiVersion: v1
kind: ConfigMap
metadata: {name: extra, namespace: ` + pruneNS + `}
data: {x: "1"}
`
	withoutCM := `apiVersion: apps/v1
kind: Deployment
metadata: {name: web, namespace: ` + pruneNS + `}
spec:
  replicas: 1
  selector: {matchLabels: {app: web}}
  template:
    metadata: {labels: {app: web}}
    spec:
      containers: [{name: pause, image: registry.k8s.io/pause:3.9}]
`
	if _, err := tgt.Apply(ctx, targetv2.ApplyRequest{
		Desired:        targetv2.DesiredState{Kind: "kubernetes", Spec: []byte(withCM), Checksum: "v1"},
		IdempotencyKey: targetv2.IdempotencyKeyFor("integration", "prune-with-cm"),
	}); err != nil {
		t.Fatalf("apply with cm: %v", err)
	}
	if out, _ := kubectlGet("get", "configmap", "extra", "-n", pruneNS, "-o", "name"); out == "" {
		t.Fatal("configmap should exist after first apply")
	}

	// Remove the configmap from desired → prune deletes it.
	if _, err := tgt.Apply(ctx, targetv2.ApplyRequest{
		Desired:        targetv2.DesiredState{Kind: "kubernetes", Spec: []byte(withoutCM), Checksum: "v2"},
		IdempotencyKey: targetv2.IdempotencyKeyFor("integration", "prune-without-cm"),
	}); err != nil {
		t.Fatalf("apply without cm: %v", err)
	}
	if out, err := kubectlGet("get", "configmap", "extra", "-n", pruneNS, "-o", "name"); err == nil && out != "" {
		t.Errorf("configmap should have been pruned, still present: %q", out)
	}
}
