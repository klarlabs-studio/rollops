package kubernetes

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// stubKubectl puts a kubectl on PATH that records its arguments and answers
// the two questions these tests ask: a resource's progress deadline, and a
// diff that — like a real one against last-applied-configuration — shows the
// rollops label being removed unless the diffed manifest carries it.
func stubKubectl(t *testing.T, progressDeadline string) (argsLog string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("stub kubectl is a shell script")
	}
	dir := t.TempDir()
	argsLog = filepath.Join(dir, "args.log")
	script := `#!/bin/sh
echo "$@" >> "` + argsLog + `"
case "$*" in
  *progressDeadlineSeconds*) printf '%s' "` + progressDeadline + `" ;;
  *"rollout status"*) exit 0 ;;
  *"diff -f -"*)
    if grep -q "` + PruneLabel + `" ; then exit 0; fi
    echo "-    ` + PruneLabel + `: demo"; exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argsLog
}

func loggedArgs(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A rollout still draining old pods is not failing. The wait is bounded by
// the resource's own progress deadline, not a fixed 30s that aborted a healthy
// production canary while old replicas finished terminating.
func TestHealthy_WaitsUpToTheProgressDeadline(t *testing.T) {
	log := stubKubectl(t, "120")
	k := &kubectlCluster{resource: "deployment/api"}
	if ok, reason, err := k.Healthy(context.Background()); !ok || err != nil {
		t.Fatalf("healthy rollout: ok=%v reason=%q err=%v", ok, reason, err)
	}
	if got := loggedArgs(t, log); !strings.Contains(got, "rollout status deployment/api --timeout=150s") {
		t.Errorf("rollout status not bounded by the 120s deadline (+30s): %s", got)
	}
}

func TestHealthy_UsesKubernetesDefaultDeadlineWhenUnset(t *testing.T) {
	log := stubKubectl(t, "")
	k := &kubectlCluster{resource: "statefulset/db"}
	if ok, _, _ := k.Healthy(context.Background()); !ok {
		t.Fatal("healthy rollout reported unhealthy")
	}
	if got := loggedArgs(t, log); !strings.Contains(got, "--timeout=630s") {
		t.Errorf("want Kubernetes' 600s default (+30s): %s", got)
	}
}

// Diff must compare what Apply would send. Apply labels every resource, so an
// unlabelled diff reported the label's removal as drift on every in-sync
// target — and would have disabled auto-rollback everywhere.
func TestDiff_ComparesTheLabelledManifestApplySends(t *testing.T) {
	stubKubectl(t, "600")
	k := &kubectlCluster{resource: "deployment/api", pruneVal: "demo"}
	manifest := []byte("apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: api\n")
	diff, err := k.Diff(context.Background(), manifest)
	if err != nil || strings.TrimSpace(diff) != "" {
		t.Errorf("an in-sync target diffed as %q (err %v)", diff, err)
	}
}
