package pluginhost

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// buildProbePlugin compiles the shared test helper binary once per run.
var buildProbePlugin = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp("", "rollops-probeplugin-")
	if err != nil {
		return "", err
	}
	bin := filepath.Join(dir, "probeplugin")
	cmd := exec.Command("go", "build", "-o", bin, "./testdata/probeplugin")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", &exec.ExitError{ProcessState: cmd.ProcessState, Stderr: out}
	}
	return bin, nil
})

func probeBinary(t *testing.T) string {
	t.Helper()
	bin, err := buildProbePlugin()
	if err != nil {
		t.Fatalf("build probeplugin: %v", err)
	}
	return bin
}

func TestBuildPluginEnv(t *testing.T) {
	parent := []string{"PATH=/bin", "HOME=/root", "TMPDIR=/tmp", "SECRET=xyz", "DD_API_KEY=k", "MALFORMED"}

	has := func(env []string, want string) bool {
		for _, kv := range env {
			if kv == want {
				return true
			}
		}
		return false
	}

	// Default deny (empty allow-list): only the essential set survives; the
	// daemon's secrets are withheld.
	got := buildPluginEnv(nil, parent)
	for _, want := range []string{"PATH=/bin", "HOME=/root", "TMPDIR=/tmp"} {
		if !has(got, want) {
			t.Errorf("essential var %q missing from confined env: %v", want, got)
		}
	}
	for _, secret := range []string{"SECRET=xyz", "DD_API_KEY=k"} {
		if has(got, secret) {
			t.Errorf("secret %q leaked into confined env: %v", secret, got)
		}
	}
	if has(got, "MALFORMED") {
		t.Errorf("malformed (no '=') entry must be dropped: %v", got)
	}

	// A named allow-list forwards exactly that var plus the essentials.
	got = buildPluginEnv([]string{"DD_API_KEY"}, parent)
	if !has(got, "DD_API_KEY=k") {
		t.Errorf("allow-listed var missing: %v", got)
	}
	if !has(got, "PATH=/bin") {
		t.Errorf("essentials must still be present alongside allow-list: %v", got)
	}
	if has(got, "SECRET=xyz") {
		t.Errorf("non-allow-listed secret must stay withheld: %v", got)
	}

	// Wildcard is the explicit escape hatch: inherit the full parent env.
	got = buildPluginEnv([]string{"*"}, parent)
	if len(got) != len(parent) {
		t.Errorf("wildcard must inherit the full environment: got %d, want %d", len(got), len(parent))
	}
	for _, want := range parent {
		if !has(got, want) {
			t.Errorf("wildcard dropped %q", want)
		}
	}
}

// TestLaunch_ConfinesEnvironment proves a launched plugin does not inherit a
// secret env var set on the parent daemon: with a restrictive allow-list the
// plugin sees only the essential set plus the explicitly allowed variable.
func TestLaunch_ConfinesEnvironment(t *testing.T) {
	bin := probeBinary(t)
	dump := filepath.Join(t.TempDir(), "env.txt")
	t.Setenv("PROBE_ENV_DUMP_FILE", dump)
	t.Setenv("ROLLOPS_PROBE_SECRET", "super-secret-token")

	// Allow only the dump-file var through; the secret is deliberately omitted.
	// A generous bound rather than the 10s default. This test exec's a real
	// subprocess, and on a machine compiling the rest of the suite in parallel a
	// freshly started process can take longer than that just to be scheduled — which
	// produced a failure indistinguishable from a broken plugin. The wait is still
	// finite, so a genuinely silent plugin still fails.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	proc, err := Launch(ctx, bin, []string{"PROBE_ENV_DUMP_FILE"})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer func() { _ = proc.Close() }()

	data, err := os.ReadFile(dump)
	if err != nil {
		t.Fatalf("read env dump: %v", err)
	}
	got := string(data)
	if strings.Contains(got, "ROLLOPS_PROBE_SECRET") || strings.Contains(got, "super-secret-token") {
		t.Errorf("plugin inherited a parent secret env var:\n%s", got)
	}
	if !strings.Contains(got, "PROBE_ENV_DUMP_FILE=") {
		t.Errorf("allow-listed var missing from plugin env:\n%s", got)
	}
	if !strings.Contains(got, "PATH=") {
		t.Errorf("essential PATH missing from plugin env:\n%s", got)
	}
}
