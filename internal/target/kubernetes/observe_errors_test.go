package kubernetes

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// stubFailingKubectl puts a kubectl on PATH whose every `get` fails with the
// given stderr and a non-zero status.
func stubFailingKubectl(t *testing.T, stderr string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("stub kubectl is a shell script")
	}
	dir := t.TempDir()
	script := `#!/bin/sh
case "$*" in
  *get*) echo "` + stderr + `" >&2; exit 1 ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// A query that FAILED must not be reported as a resource that is ABSENT.
//
// LiveChecksum discarded every error and returned "", which the planner reads
// as "no current state observed" — a create. So a revoked RBAC rule, an
// unreachable API server or a throttled request looked exactly like a workload
// that had never been deployed, and rollops would re-apply a live target every
// reconcile interval with nothing logged: no error, no drift alert, just a
// rollout a minute against a cluster it could not actually see.
func TestLiveChecksum_FailedQueryIsAnError(t *testing.T) {
	stubFailingKubectl(t, `Error from server (Forbidden): deployments.apps \"api\" is forbidden`)
	k := &kubectlCluster{resource: "deployment/api", namespace: "prod"}

	if _, err := k.LiveChecksum(context.Background()); err == nil {
		t.Fatal("a Forbidden get reported no error: the planner would read this as a create and re-apply forever")
	}

	// And it must reach the caller, not be swallowed one layer up.
	tgt := &Target{cl: k}
	if _, err := tgt.Observe(context.Background()); err == nil {
		t.Error("Observe reported no error for a failed query")
	} else if !strings.Contains(err.Error(), "observe") {
		t.Errorf("error does not say what failed: %v", err)
	}
}

// An absent resource is still not an error: that is a genuine empty
// observation, and the planner is right to call it a create.
func TestLiveChecksum_AbsentResourceIsEmptyNotAnError(t *testing.T) {
	stubFailingKubectl(t, `Error from server (NotFound): deployments.apps \"api\" not found`)
	k := &kubectlCluster{resource: "deployment/api", namespace: "prod"}

	got, err := k.LiveChecksum(context.Background())
	if err != nil {
		t.Fatalf("an absent resource must not be an error: %v", err)
	}
	if got != "" {
		t.Errorf("checksum = %q, want empty", got)
	}
}

// LiveYAML feeds Diff's ignoreDifferences filtering, and had the same hole: a
// failed get returned empty YAML, which reads as "nothing live to compare".
func TestLiveYAML_FailedQueryIsAnError(t *testing.T) {
	stubFailingKubectl(t, `Error from server (Forbidden): deployments.apps \"api\" is forbidden`)
	k := &kubectlCluster{resource: "deployment/api", namespace: "prod"}

	if _, err := k.LiveYAML(context.Background()); err == nil {
		t.Error("a Forbidden get reported no error")
	}
}

func TestLiveYAML_AbsentResourceIsEmptyNotAnError(t *testing.T) {
	stubFailingKubectl(t, `Error from server (NotFound): deployments.apps \"api\" not found`)
	k := &kubectlCluster{resource: "deployment/api", namespace: "prod"}

	out, err := k.LiveYAML(context.Background())
	if err != nil {
		t.Fatalf("an absent resource must not be an error: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("yaml = %q, want empty", out)
	}
}
