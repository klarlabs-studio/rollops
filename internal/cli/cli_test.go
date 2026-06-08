package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/rolloffs/internal/config"
	"go.klarlabs.de/rolloffs/internal/engine"
	"go.klarlabs.de/rolloffs/internal/rollout"
	"go.klarlabs.de/rolloffs/internal/store/sqlite"
	itarget "go.klarlabs.de/rolloffs/internal/target"
	pt "go.klarlabs.de/rolloffs/pkg/target"
)

const cfgYAML = `
apiVersion: rolloffs.klarlabs.de/v1
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
`

type fakeTarget struct{ applied int }

func (f *fakeTarget) Apply(context.Context, pt.Manifest) (pt.Result, error) {
	f.applied++
	return pt.Result{Changed: true}, nil
}
func (f *fakeTarget) Observe(context.Context) (pt.Fingerprint, error) {
	return pt.Fingerprint{}, nil
}
func (f *fakeTarget) Health(context.Context) (pt.HealthStatus, error) {
	return pt.HealthStatus{State: pt.HealthHealthy}, nil
}

func newApp(t *testing.T) (*App, *bytes.Buffer, string) {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	reg := itarget.NewRegistry()
	reg.Register("fake", func(config.Target) (pt.Target, error) { return &fakeTarget{}, nil })
	clock := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	eng := engine.New(db, reg, engine.WithClock(func() time.Time { return clock }), engine.WithIDGen(func() string { return "ro-cli" }))

	var buf bytes.Buffer
	app := &App{Ops: eng, Out: &buf, Actor: rollout.Identity{Kind: "human", Name: "felix"}}

	cfgPath := filepath.Join(t.TempDir(), "rolloffs.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	return app, &buf, cfgPath
}

func TestCLI_Plan(t *testing.T) {
	app, buf, cfg := newApp(t)
	if err := app.Run(context.Background(), []string{"plan", cfg}); err != nil {
		t.Fatalf("plan: %v", err)
	}
	if !strings.Contains(buf.String(), "demo/prod/app") {
		t.Errorf("plan output = %q", buf.String())
	}
}

func TestCLI_ApplyThenStatus(t *testing.T) {
	app, buf, cfg := newApp(t)
	if err := app.Run(context.Background(), []string{"apply", cfg}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !strings.Contains(buf.String(), "ro-cli") {
		t.Errorf("apply output = %q", buf.String())
	}
	buf.Reset()
	if err := app.Run(context.Background(), []string{"status", "ro-cli"}); err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(buf.String(), "verifying") {
		t.Errorf("status output = %q", buf.String())
	}
}

func TestCLI_UnknownCommand(t *testing.T) {
	app, _, _ := newApp(t)
	if err := app.Run(context.Background(), []string{"frobnicate"}); err == nil {
		t.Fatal("unknown command should error")
	}
}

func TestCLI_StatusRequiresID(t *testing.T) {
	app, _, _ := newApp(t)
	if err := app.Run(context.Background(), []string{"status"}); err == nil {
		t.Fatal("status without id should error")
	}
}
