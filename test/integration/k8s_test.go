//go:build integration

package integration

import (
	"context"
	"os/exec"
	"testing"

	"go.klarlabs.de/rolloffs/internal/config"
	"go.klarlabs.de/rolloffs/internal/target/kubernetes"
	pt "go.klarlabs.de/rolloffs/pkg/target"
)

// A tiny always-ready Deployment (pause container — no real workload needed to
// exercise apply / annotate / observe / rollout-status).
const echoDeployment = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: echo
  namespace: rolloffs-it
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
	_ = exec.Command("kubectl", "create", "namespace", "rolloffs-it").Run()
	t.Cleanup(func() { _ = exec.Command("kubectl", "delete", "namespace", "rolloffs-it", "--wait=false").Run() })

	cfg := config.Target{
		Kind: "kubernetes",
		Ref:  "int/k8s",
		Spec: map[string]any{
			"context":   ctxName,
			"namespace": "rolloffs-it",
			"resource":  "deployment/echo",
		},
	}
	tgt, err := kubernetes.New(cfg)
	if err != nil {
		t.Fatalf("new k8s target: %v", err)
	}
	ctx := context.Background()
	m := pt.Manifest{Kind: "kubernetes", Spec: []byte(echoDeployment), Checksum: "k8s-live-v1"}

	// Apply: kubectl apply + annotate the live resource with the checksum.
	res, err := tgt.Apply(ctx, m)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !res.Changed {
		t.Error("first apply should report changed")
	}

	// Observe reads the checksum annotation back from the LIVE cluster (rich).
	fp, err := tgt.Observe(ctx)
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if fp.Value != "k8s-live-v1" {
		t.Errorf("live cluster observed %q, want k8s-live-v1", fp.Value)
	}

	// Idempotent: re-applying the same checksum is a no-op.
	res2, err := tgt.Apply(ctx, m)
	if err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	if res2.Changed {
		t.Error("re-applying the same checksum must be a no-op")
	}

	// Health: rollout status of the deployment.
	hs, err := tgt.Health(ctx)
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if hs.State != pt.HealthHealthy {
		t.Logf("health = %v (%s) — acceptable if rollout still progressing", hs.State, hs.Reason)
	}
}
