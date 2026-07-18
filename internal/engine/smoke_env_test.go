package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.klarlabs.de/rollops/internal/config"
	"go.klarlabs.de/rollops/internal/security"
	pt "go.klarlabs.de/rollops/pkg/target"
)

// writeEnvDumper writes a shell script that dumps its own environment to a file,
// so a test can inspect what a real spawned smoke command actually inherited.
func writeEnvDumper(t *testing.T, dir string) (script, dump string) {
	t.Helper()
	dump = filepath.Join(dir, "env.txt")
	script = filepath.Join(dir, "dump-env.sh")
	body := "#!/bin/sh\nenv > " + dump + "\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return script, dump
}

// TestExecSmoke_DoesNotInheritDaemonSecrets spawns a REAL smoke command and
// reads the environment it actually received. The repo config names this
// command, so it must not be able to read the daemon's secrets.
func TestExecSmoke_DoesNotInheritDaemonSecrets(t *testing.T) {
	dir := t.TempDir()
	script, dump := writeEnvDumper(t, dir)

	// Secrets as the daemon would carry them.
	t.Setenv("ROLLOPS_MCP_TOKENS", `{"tok-a":"nomi"}`)
	t.Setenv("ROLLOPS_ADMIN_TOKEN", "super-secret-admin")
	t.Setenv("ROLLOPS_UI_PASSWORD", "hunter2")

	runner := execSmoke{confinement: security.ConfinementFromEnv(func(string) string { return "" })}
	code, err := runner.Run(context.Background(), []string{script})
	if err != nil {
		t.Fatalf("run smoke: %v", err)
	}
	if code != 0 {
		t.Fatalf("smoke exit = %d, want 0", code)
	}
	got, err := os.ReadFile(dump)
	if err != nil {
		t.Fatalf("read dumped env: %v", err)
	}
	for _, secret := range []string{"super-secret-admin", "hunter2", "tok-a"} {
		if strings.Contains(string(got), secret) {
			t.Errorf("smoke command inherited the daemon secret %q:\n%s", secret, got)
		}
	}
	if !strings.Contains(string(got), "PATH=") {
		t.Errorf("smoke command should still get PATH:\n%s", got)
	}
}

// TestExecSmoke_AllowListForwards proves an operator can still hand a smoke test
// the variable it legitimately needs.
func TestExecSmoke_AllowListForwards(t *testing.T) {
	dir := t.TempDir()
	script, dump := writeEnvDumper(t, dir)
	t.Setenv("ROLLOPS_ADMIN_TOKEN", "super-secret-admin")
	t.Setenv("SMOKE_BASE_URL", "https://staging.example.com")

	runner := execSmoke{confinement: security.ConfinementFromEnv(func(k string) string {
		if k == "ROLLOPS_ALLOWED_ENV" {
			return "SMOKE_BASE_URL"
		}
		return ""
	})}
	if _, err := runner.Run(context.Background(), []string{script}); err != nil {
		t.Fatalf("run smoke: %v", err)
	}
	got, err := os.ReadFile(dump)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "SMOKE_BASE_URL=https://staging.example.com") {
		t.Errorf("allow-listed var should reach the smoke command:\n%s", got)
	}
	if strings.Contains(string(got), "super-secret-admin") {
		t.Errorf("non-allow-listed secret still leaked:\n%s", got)
	}
}

// TestExecDBRollback_DoesNotInheritDaemonSecrets covers the other config-sourced
// command path — database migrate/rollback hooks run the same way.
func TestExecDBRollback_DoesNotInheritDaemonSecrets(t *testing.T) {
	dir := t.TempDir()
	script, dump := writeEnvDumper(t, dir)
	t.Setenv("ROLLOPS_ADMIN_TOKEN", "super-secret-admin")

	runner := execDBRollback{confinement: security.ConfinementFromEnv(func(string) string { return "" })}
	if err := runner.Run(context.Background(), []string{script}); err != nil {
		t.Fatalf("run db hook: %v", err)
	}
	got, err := os.ReadFile(dump)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "super-secret-admin") {
		t.Errorf("database hook inherited a daemon secret:\n%s", got)
	}
}

// TestEngine_SmokeGateConfinesEnvEndToEnd drives the scrub through the engine's
// real post-deploy gate rather than the runner in isolation, so the confinement
// wiring (WithConfinement → execSmoke) is covered too.
func TestEngine_SmokeGateConfinesEnvEndToEnd(t *testing.T) {
	dir := t.TempDir()
	script, dump := writeEnvDumper(t, dir)
	t.Setenv("ROLLOPS_ADMIN_TOKEN", "super-secret-admin")

	yaml := `
apiVersion: rollops.klarlabs.de/v1
kind: RolloutConfig
metadata:
  name: demo
spec:
  target:
    kind: fake
    ref: demo/prod/app
    criticality: low
    spec:
      x: 1
  strategy:
    type: rolling
  rollback:
    auto: true
    smokeTest:
      command: ["` + script + `"]
      expectExit: 0
`
	c, err := config.Load([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}}
	// WithConfinement installs the real exec-backed runners.
	e, _ := newEngine(t, fake, WithConfinement(security.ConfinementFromEnv(func(string) string { return "" })))
	ctx := context.Background()
	r, err := e.Apply(ctx, ApplyRequest{Config: c})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	out, err := e.VerifyOrRollback(ctx, r.ID, pt.Manifest{Kind: "fake", Checksum: "prior"}, c)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if out.RolledBack {
		t.Fatalf("smoke script exits 0, should promote; reason=%q", out.Reason)
	}
	got, err := os.ReadFile(dump)
	if err != nil {
		t.Fatalf("smoke command did not run: %v", err)
	}
	if strings.Contains(string(got), "super-secret-admin") {
		t.Errorf("smoke command run through the engine inherited a daemon secret:\n%s", got)
	}
}
