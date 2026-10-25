package kubernetes

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// kubectlExiting puts a kubectl on PATH that prints stdout and stderr and
// exits with code.
func kubectlExiting(t *testing.T, code, stdout, stderr string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("stub kubectl is a shell script")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\ncat >/dev/null\nprintf '%s' '" + stdout + "'\nprintf '%s\\n' '" + stderr + "' >&2\nexit " + code + "\n"
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestDiff_ExitZeroIsInSync(t *testing.T) {
	kubectlExiting(t, "0", "", "")
	got, err := (&kubectlCluster{resource: "deployment/api"}).Diff(context.Background(), []byte("kind: Deployment\n"))
	if err != nil || got != "" {
		t.Fatalf("Diff = %q, %v; want empty, nil", got, err)
	}
}

func TestDiff_ExitOneIsTheDiff(t *testing.T) {
	kubectlExiting(t, "1", "-  replicas: 2\n+  replicas: 3\n", "")
	got, err := (&kubectlCluster{resource: "deployment/api"}).Diff(context.Background(), []byte("kind: Deployment\n"))
	if err != nil || got == "" {
		t.Fatalf("Diff = %q, %v; want the diff, nil", got, err)
	}
}

// A failed diff printed nothing on stdout and read as "in sync". The
// consumers then trusted an unknown live state: rollbackBaseline accepted it
// as a verified baseline, full verification could never fail closed, and
// Apply skipped the apply as already at desired.
func TestDiff_ErrorIsNotInSync(t *testing.T) {
	kubectlExiting(t, "2", "", "error: Unable to connect to the server: dial tcp 10.43.0.1:443: connect: connection refused")
	got, err := (&kubectlCluster{resource: "deployment/api"}).Diff(context.Background(), []byte("kind: Deployment\n"))
	if err == nil {
		t.Fatalf("Diff = %q, nil; want an error", got)
	}
}
