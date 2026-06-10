package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/rollops/internal/config"
	"go.klarlabs.de/rollops/internal/engine"
	"go.klarlabs.de/rollops/internal/notify"
	"go.klarlabs.de/rollops/internal/rollout"
	"go.klarlabs.de/rollops/internal/store/sqlite"
	itarget "go.klarlabs.de/rollops/internal/target"
	pt "go.klarlabs.de/rollops/pkg/target"
)

const cfgYAML = `
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
`

type fakeTarget struct {
	applied   int
	manifests []pt.Manifest
}

func (f *fakeTarget) Apply(_ context.Context, m pt.Manifest) (pt.Result, error) {
	f.applied++
	f.manifests = append(f.manifests, m)
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
	return newAppWithTarget(t, &fakeTarget{}, func() string { return "ro-cli" })
}

func newAppWithTarget(t *testing.T, fake *fakeTarget, idgen func() string) (*App, *bytes.Buffer, string) {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	reg := itarget.NewRegistry()
	reg.Register("fake", func(config.Target) (pt.Target, error) { return fake, nil })
	clock := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	tick := 0
	eng := engine.New(db, reg, engine.WithClock(func() time.Time {
		tick++
		return clock.Add(time.Duration(tick) * time.Second)
	}), engine.WithIDGen(idgen))

	var buf bytes.Buffer
	app := &App{Ops: eng, Out: &buf, Actor: rollout.Identity{Kind: "human", Name: "felix"}}

	cfgPath := filepath.Join(t.TempDir(), "rollops.yaml")
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

func TestCLI_StatusShowsLatestHistoryNote(t *testing.T) {
	var buf bytes.Buffer
	app := &App{Ops: statusNoteOps{}, Out: &buf}
	buf.Reset()
	if err := app.Run(context.Background(), []string{"status", "ro-cli"}); err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(buf.String(), "database rollback: succeeded") {
		t.Fatalf("status output = %q", buf.String())
	}
}

type statusNoteOps struct{}

func (statusNoteOps) Plan(context.Context, *config.Config) (*engine.Plan, error) { return nil, nil }
func (statusNoteOps) Apply(context.Context, engine.ApplyRequest) (*rollout.Rollout, error) {
	return nil, nil
}
func (statusNoteOps) Status(context.Context, string) (rollout.Rollout, error) {
	return rollout.Rollout{ID: "ro-cli", TargetRef: "demo/prod/app", Phase: rollout.PhaseRolledBack, Strategy: rollout.StrategyRolling}, nil
}
func (statusNoteOps) Promote(context.Context, string) (rollout.Rollout, error) {
	return rollout.Rollout{}, nil
}
func (statusNoteOps) RollbackLast(context.Context, string) (rollout.Rollout, error) {
	return rollout.Rollout{}, nil
}
func (statusNoteOps) History(context.Context, string) ([]rollout.RolloutRecord, error) {
	return []rollout.RolloutRecord{{RolloutID: "ro-cli", Note: "database rollback: succeeded"}}, nil
}

func TestCLI_RollbackLast(t *testing.T) {
	fake := &fakeTarget{}
	n := 0
	app, buf, cfg := newAppWithTarget(t, fake, func() string {
		n++
		return "ro-cli-" + string(rune('0'+n))
	})

	if err := app.Run(context.Background(), []string{"apply", cfg}); err != nil {
		t.Fatalf("apply first: %v", err)
	}
	first := fake.manifests[len(fake.manifests)-1]

	data := strings.Replace(cfgYAML, "x: 1", "x: 2", 1)
	cfg2 := filepath.Join(t.TempDir(), "rollops-2.yaml")
	if err := os.WriteFile(cfg2, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := app.Run(context.Background(), []string{"apply", cfg2}); err != nil {
		t.Fatalf("apply second: %v", err)
	}

	buf.Reset()
	if err := app.Run(context.Background(), []string{"rollback", "demo/prod/app"}); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if !strings.Contains(buf.String(), "rolled-back") {
		t.Errorf("rollback output = %q", buf.String())
	}
	last := fake.manifests[len(fake.manifests)-1]
	if last.Checksum != first.Checksum {
		t.Errorf("rollback applied checksum %q, want previous %q", last.Checksum, first.Checksum)
	}
}

func TestCLI_UnknownCommand(t *testing.T) {
	app, _, _ := newApp(t)
	if err := app.Run(context.Background(), []string{"frobnicate"}); err == nil {
		t.Fatal("unknown command should error")
	}
}

func TestCLI_Version(t *testing.T) {
	app, buf, _ := newApp(t)
	if err := app.Run(context.Background(), []string{"version"}); err != nil {
		t.Fatalf("version: %v", err)
	}
	if !strings.Contains(buf.String(), "rollops ") {
		t.Errorf("version output = %q", buf.String())
	}
}

func TestCLI_StatusRequiresID(t *testing.T) {
	app, _, _ := newApp(t)
	if err := app.Run(context.Background(), []string{"status"}); err == nil {
		t.Fatal("status without id should error")
	}
}

func TestCLI_RollbackRequiresTargetRef(t *testing.T) {
	app, _, _ := newApp(t)
	if err := app.Run(context.Background(), []string{"rollback"}); err == nil {
		t.Fatal("rollback without target ref should error")
	}
}

func TestCLI_DoctorLocal(t *testing.T) {
	app, buf, cfg := newApp(t)
	app.Doctor.DBPath = filepath.Join(t.TempDir(), "doctor.db")
	if err := app.Run(context.Background(), []string{"doctor", cfg}); err != nil {
		t.Fatalf("doctor: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "config: ok") || !strings.Contains(out, "database: ok") {
		t.Errorf("doctor output = %q", out)
	}
}

func TestCLI_DoctorFailsInvalidConfig(t *testing.T) {
	app, buf, _ := newApp(t)
	app.Doctor.DBPath = filepath.Join(t.TempDir(), "doctor.db")
	bad := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(bad, []byte("not: rollops\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := app.Run(context.Background(), []string{"doctor", bad}); err == nil {
		t.Fatal("doctor must fail invalid config")
	}
	if !strings.Contains(buf.String(), "config: fail") {
		t.Errorf("doctor output = %q", buf.String())
	}
}

type fakeNotifier struct {
	got *notify.Event
	err error
}

func (f *fakeNotifier) Notify(_ context.Context, e notify.Event) error {
	f.got = &e
	return f.err
}

func TestCLI_DoctorNotify(t *testing.T) {
	app, buf, cfg := newApp(t)
	app.Doctor.DBPath = filepath.Join(t.TempDir(), "doctor.db")
	fn := &fakeNotifier{}
	app.Doctor.Notifier = fn
	app.Doctor.NotifyChannels = []string{"email"}
	if err := app.Run(context.Background(), []string{"doctor", cfg}); err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if fn.got == nil || fn.got.Kind != notify.Test {
		t.Fatalf("test event not sent, got %+v", fn.got)
	}
	if !strings.Contains(buf.String(), "notify: ok (email)") {
		t.Errorf("doctor output = %q", buf.String())
	}
}

func TestCLI_DoctorNotifyFails(t *testing.T) {
	app, buf, cfg := newApp(t)
	app.Doctor.DBPath = filepath.Join(t.TempDir(), "doctor.db")
	app.Doctor.Notifier = &fakeNotifier{err: errors.New("smtp connection refused")}
	app.Doctor.NotifyChannels = []string{"email"}
	if err := app.Run(context.Background(), []string{"doctor", cfg}); err == nil {
		t.Fatal("doctor must fail when a notify channel fails")
	}
	if !strings.Contains(buf.String(), "notify: fail") {
		t.Errorf("doctor output = %q", buf.String())
	}
}

func TestCLI_DoctorNotifySkippedWhenUnconfigured(t *testing.T) {
	app, buf, cfg := newApp(t)
	app.Doctor.DBPath = filepath.Join(t.TempDir(), "doctor.db")
	if err := app.Run(context.Background(), []string{"doctor", cfg}); err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if !strings.Contains(buf.String(), "notify: skipped") {
		t.Errorf("doctor output = %q", buf.String())
	}
}

func TestCLI_DoctorDaemon(t *testing.T) {
	app, buf, cfg := newApp(t)
	var gotAddr, gotToken string
	app.Doctor = Doctor{
		DaemonAddr: "127.0.0.1:8090",
		Token:      "devtoken",
		Probe: func(_ context.Context, addr, token string) error {
			gotAddr, gotToken = addr, token
			return nil
		},
	}
	if err := app.Run(context.Background(), []string{"doctor", cfg}); err != nil {
		t.Fatalf("doctor daemon: %v", err)
	}
	if gotAddr != "127.0.0.1:8090" || gotToken != "devtoken" {
		t.Fatalf("probe got addr=%q token=%q", gotAddr, gotToken)
	}
	if !strings.Contains(buf.String(), "daemon: ok") {
		t.Errorf("doctor output = %q", buf.String())
	}
}
