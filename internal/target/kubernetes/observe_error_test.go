package kubernetes

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// kubectlFailing puts a kubectl on PATH that prints stderr and exits 1.
func kubectlFailing(t *testing.T, stderr string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("stub kubectl is a shell script")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\nprintf '%s\\n' '" + stderr + "' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestLiveChecksum_AbsentResourceIsEmpty(t *testing.T) {
	kubectlFailing(t, `Error from server (NotFound): deployments.apps "api" not found`)
	k := &kubectlCluster{resource: "deployment/api"}
	got, err := k.LiveChecksum(context.Background())
	if err != nil || got != "" {
		t.Fatalf("LiveChecksum = %q, %v; want empty, nil for a resource that does not exist", got, err)
	}
}

// 2026-10-24 22:51: the API server refused connections, every target read as
// absent, and rollops re-applied 14 unchanged vorhut manifests, failed, and
// recorded 14 rollbacks. An unreachable API server is not an absent resource.
func TestLiveChecksum_UnreachableAPIServerIsAnError(t *testing.T) {
	for _, stderr := range []string{
		`The connection to the server 10.43.0.1:443 was refused - did you specify the right host or port?`,
		`Unable to connect to the server: dial tcp 10.43.0.1:443: connect: connection refused`,
		`Error from server (Forbidden): deployments.apps "api" is forbidden: User "x" cannot get resource`,
		`Error from server (ServiceUnavailable): the server is currently unable to handle the request`,
	} {
		kubectlFailing(t, stderr)
		k := &kubectlCluster{resource: "deployment/api"}
		if got, err := k.LiveChecksum(context.Background()); err == nil {
			t.Errorf("stderr %q: LiveChecksum = %q, nil; want an error", stderr, got)
		}
		if got, err := k.LiveYAML(context.Background()); err == nil {
			t.Errorf("stderr %q: LiveYAML = %q, nil; want an error", stderr, got)
		}
	}
}

func TestLiveYAML_AbsentResourceIsEmpty(t *testing.T) {
	kubectlFailing(t, `Error from server (NotFound): deployments.apps "api" not found`)
	k := &kubectlCluster{resource: "deployment/api"}
	got, err := k.LiveYAML(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatalf("LiveYAML = %q, %v; want empty, nil", got, err)
	}
}
