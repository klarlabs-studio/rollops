//go:build integration

package integration

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"go.klarlabs.de/rolloffs/internal/audit"
	"go.klarlabs.de/rolloffs/internal/config"
	"go.klarlabs.de/rolloffs/internal/engine"
	"go.klarlabs.de/rolloffs/internal/reconcile"
	"go.klarlabs.de/rolloffs/internal/rollout"
	"go.klarlabs.de/rolloffs/internal/secrets"
	"go.klarlabs.de/rolloffs/internal/security"
	"go.klarlabs.de/rolloffs/internal/store/sqlite"
	"go.klarlabs.de/rolloffs/internal/target"
)

const dogfoodConfig = `apiVersion: rolloffs.klarlabs.de/v1
kind: RolloutConfig
metadata:
  name: dogfood
spec:
  target:
    kind: kubernetes
    ref: dogfood/it/web
    criticality: medium
    spec:
      context: minikube
      namespace: rolloffs-dogfood-it
      resource: deployment/web
      manifest: |
        apiVersion: apps/v1
        kind: Deployment
        metadata:
          name: web
          namespace: rolloffs-dogfood-it
        spec:
          replicas: 1
          selector:
            matchLabels: {app: web}
          template:
            metadata:
              labels: {app: web}
            spec:
              containers:
                - name: pause
                  image: registry.k8s.io/pause:3.9
  strategy:
    type: rolling
`

func gitInit(t *testing.T, dir, content string) {
	t.Helper()
	write := func(p, c string) {
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-b", "main")
	write(filepath.Join(dir, "rolloffs.yaml"), content)
	run("add", ".")
	run("commit", "-m", "dogfood")
}

func kubectlGet(args ...string) (string, error) {
	out, err := exec.Command("kubectl", args...).Output()
	return string(out), err
}

// TestDogfood_GitToClusterReconcile is the Phase-0 MVP flow end to end:
// a git repo with rolloffs.yaml -> the reconcile watcher (daemon brain) over the
// FULL enforced engine (audit + guardrails + secrets) -> a real deploy to
// minikube -> drift (delete) -> reconciled back -> promoted.
func TestDogfood_GitToClusterReconcile(t *testing.T) {
	ctxName := kubeCtx(t) // skips if no reachable cluster
	_ = ctxName
	ctx := context.Background()

	_ = exec.Command("kubectl", "create", "namespace", "rolloffs-dogfood-it").Run()
	t.Cleanup(func() {
		_ = exec.Command("kubectl", "delete", "namespace", "rolloffs-dogfood-it", "--wait=false").Run()
	})

	// Upstream git repo carrying the declarative config.
	upstream := t.TempDir()
	gitInit(t, upstream, dogfoodConfig)

	// Full enforced engine, exactly as the daemon builds it.
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	eng := engine.New(db, target.Builtin(),
		engine.WithAudit(audit.New(os.Stderr)),
		engine.WithGuardrails(&security.Guardrails{Floor: security.DefaultPolicyFloor(), Freeze: security.NewFreeze()}),
		engine.WithSecrets(secrets.EnvProvider{Prefix: "ROLLOFFS_SECRET_"}),
	)
	rec := reconcile.New(eng, audit.New(os.Stderr))
	watcher, err := reconcile.NewWatcher(ctx, rec, t.TempDir(), []reconcile.RepoSpec{{
		Name:      "dogfood",
		URL:       "file://" + upstream,
		Ref:       config.RepoRef{Branch: "main", Path: "rolloffs.yaml"},
		Initiator: rollout.Identity{Kind: "ci", Name: "reconciler"},
	}})
	if err != nil {
		t.Fatalf("watcher: %v", err)
	}

	// Tick 1: deploy from git to the cluster.
	for _, o := range watcher.Tick(ctx) {
		if o.Err != nil {
			t.Fatalf("tick(deploy): %v", o.Err)
		}
		if !o.Outcome.Reconciled {
			t.Fatalf("first tick should deploy: %+v", o.Outcome)
		}
	}
	if out, err := kubectlGet("get", "deployment", "web", "-n", "rolloffs-dogfood-it", "-o", "name"); err != nil || out == "" {
		t.Fatalf("deployment not created on cluster: %v (%q)", err, out)
	}

	// Drift: delete the live deployment.
	if err := exec.Command("kubectl", "delete", "deployment", "web", "-n", "rolloffs-dogfood-it", "--wait=true").Run(); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// Tick 2: drift detected and reconciled back.
	var reconciled bool
	for _, o := range watcher.Tick(ctx) {
		if o.Err != nil {
			t.Fatalf("tick(reconcile): %v", o.Err)
		}
		reconciled = o.Outcome.Drift && o.Outcome.Reconciled
	}
	if !reconciled {
		t.Fatal("drift should have been detected and reconciled")
	}

	// Settle, then confirm the deployment is back with the checksum annotation.
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if out, _ := kubectlGet("get", "deployment", "web", "-n", "rolloffs-dogfood-it",
			"-o", "jsonpath={.metadata.annotations.rolloffs\\.klarlabs\\.de/checksum}"); out != "" {
			return // reconciled back with a checksum — done
		}
		time.Sleep(time.Second)
	}
	t.Fatal("deployment was not reconciled back onto the cluster")
}
