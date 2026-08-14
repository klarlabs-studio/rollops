package boot

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.klarlabs.de/rollops/internal/config"
	"go.klarlabs.de/rollops/internal/engine"
	"go.klarlabs.de/rollops/internal/rollout"
	"go.klarlabs.de/rollops/internal/store/sqlite"
	itarget "go.klarlabs.de/rollops/internal/target"
	pt "go.klarlabs.de/rollops/pkg/target"
)

const fakeYAML = `
apiVersion: rollops.klarlabs.de/v1
kind: RolloutConfig
metadata:
  name: demo
spec:
  target:
    kind: fake
    ref: demo/prod/app
    criticality: medium
    spec:
      x: 1
  strategy:
    type: rolling
`

type fakeTarget struct {
	applied int
	spec    map[string]any
	health  pt.HealthStatus
}

func (f *fakeTarget) Apply(context.Context, pt.Manifest) (pt.Result, error) {
	f.applied++
	return pt.Result{Changed: true}, nil
}
func (f *fakeTarget) Observe(context.Context) (pt.Fingerprint, error) {
	return pt.Fingerprint{Value: "fp"}, nil
}
func (f *fakeTarget) Health(context.Context) (pt.HealthStatus, error) {
	if f.health.State == 0 {
		return pt.HealthStatus{State: pt.HealthHealthy}, nil
	}
	return f.health, nil
}

func getenv(m map[string]string) Getenv {
	return func(k string) string { return m[k] }
}

func TestOptions_RequiresStore(t *testing.T) {
	_, err := Config{}.Options(context.Background())
	if err == nil {
		t.Fatal("nil store must error")
	}
}

func TestOptions_OneShotHonorsPersistedFreeze(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	fake := &fakeTarget{}
	reg := itarget.NewRegistry()
	reg.Register("fake", func(c config.Target) (pt.Target, error) { fake.spec = c.Spec; return fake, nil })

	opts, err := Config{Getenv: getenv(nil), Store: db}.Options(ctx)
	if err != nil {
		t.Fatal(err)
	}
	e := engine.New(db, reg, opts...)
	c, err := config.Load([]byte(fakeYAML))
	if err != nil {
		t.Fatal(err)
	}
	by := rollout.Identity{Kind: "human", Name: "felix"}
	if _, err := e.Apply(ctx, engine.ApplyRequest{Config: c, Initiator: by}); err != nil {
		t.Fatalf("apply before freeze: %v", err)
	}

	if _, _, err := e.Freeze(ctx, true, by, "incident-42"); err != nil {
		t.Fatal(err)
	}

	var log bytes.Buffer
	opts2, err := Config{Getenv: getenv(nil), Store: db, Log: &log}.Options(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log.String(), "freeze restored") {
		t.Errorf("boot log should mention restored freeze: %q", log.String())
	}
	e2 := engine.New(db, reg, opts2...)
	if _, err := e2.Apply(ctx, engine.ApplyRequest{Config: c, Initiator: by}); err == nil {
		t.Fatal("one-shot apply against a frozen store must be refused")
	}
}

func TestOptions_WiresGovernanceFromEnv(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var log bytes.Buffer
	_, err = Config{
		Getenv: getenv(map[string]string{"ROLLOPS_GOVERNANCE_URL": "https://gov.example/gate"}),
		Store:  db,
		Log:    &log,
	}.Options(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log.String(), "external governance") {
		t.Errorf("expected governance startup log, got %q", log.String())
	}
}

func TestOptions_WiresVaultFromEnv(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "v.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var log bytes.Buffer
	_, err = Config{
		Getenv: getenv(map[string]string{
			"VAULT_ADDR":  "https://vault.example",
			"VAULT_TOKEN": "s.supersecret",
		}),
		Store: db,
		Log:   &log,
	}.Options(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := log.String()
	if strings.Contains(got, "s.supersecret") {
		t.Fatal("vault token must not appear in boot logs")
	}
	if !strings.Contains(got, "Vault+Env") || !strings.Contains(got, "https://vault.example") {
		t.Errorf("expected Vault chain log, got %q", got)
	}
}

func TestOptions_WiresAnalysisFromEnv(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "an.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var log bytes.Buffer
	_, err = Config{
		Getenv: getenv(map[string]string{"ROLLOPS_ANALYSIS": "1"}),
		Store:  db,
		Log:    &log,
	}.Options(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log.String(), "metric analysis enabled") {
		t.Errorf("expected analysis startup log, got %q", log.String())
	}
}

func TestOptions_AnalysisOffByDefault(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var log bytes.Buffer
	_, err = Config{Getenv: getenv(nil), Store: db, Log: &log}.Options(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(log.String(), "metric analysis enabled") {
		t.Error("analysis must stay off unless ROLLOPS_ANALYSIS is set")
	}
}

func TestOptions_OneShotResolvesSecretsAndAudits(t *testing.T) {
	t.Setenv("ROLLOPS_SECRET_DEMO_TOKEN", "s3cret-value")
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	fake := &fakeTarget{}
	reg := itarget.NewRegistry()
	reg.Register("fake", func(c config.Target) (pt.Target, error) {
		fake.spec = c.Spec
		return fake, nil
	})
	var log bytes.Buffer
	opts, err := Config{Getenv: getenv(nil), Store: db, Log: &log}.Options(ctx)
	if err != nil {
		t.Fatal(err)
	}
	e := engine.New(db, reg, opts...)
	yaml := strings.Replace(fakeYAML, "x: 1", "x: 1\n      token: \"secret:demo.token\"", 1)
	c, err := config.Load([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Apply(ctx, engine.ApplyRequest{Config: c, Initiator: rollout.Identity{Kind: "human", Name: "felix"}}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if fake.spec["token"] != "s3cret-value" {
		t.Errorf("secret not resolved: %v", fake.spec["token"])
	}
	if strings.Contains(log.String(), "s3cret-value") {
		t.Error("resolved secret leaked into the audit trail")
	}
	if !strings.Contains(log.String(), `"action":"apply"`) {
		t.Errorf("apply must be audited, log=%q", log.String())
	}
}

func TestOptions_OneShotEnforcesArtifactGate(t *testing.T) {
	key := filepath.Join(t.TempDir(), "cosign.pub")
	if err := os.WriteFile(key, []byte("not-a-cosign-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	fake := &fakeTarget{}
	reg := itarget.NewRegistry()
	reg.Register("fake", func(config.Target) (pt.Target, error) { return fake, nil })
	opts, err := Config{Getenv: getenv(map[string]string{"ROLLOPS_COSIGN_KEY": key}), Store: db}.Options(ctx)
	if err != nil {
		t.Fatal(err)
	}
	e := engine.New(db, reg, opts...)
	yaml := strings.Replace(fakeYAML, "x: 1", "x: 1\n      image: ghcr.io/acme/api:1.2.3", 1)
	c, err := config.Load([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Apply(ctx, engine.ApplyRequest{Config: c, Initiator: rollout.Identity{Kind: "human", Name: "felix"}}); err == nil {
		t.Fatal("one-shot apply must honor the artifact gate when ROLLOPS_COSIGN_KEY is set")
	}
	if fake.applied != 0 {
		t.Error("unverified artifact must not reach the target")
	}
}

func TestOptions_OneShotRollbackUsesSameEngine(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	fake := &fakeTarget{}
	reg := itarget.NewRegistry()
	reg.Register("fake", func(config.Target) (pt.Target, error) { return fake, nil })
	var log bytes.Buffer
	opts, err := Config{Getenv: getenv(nil), Store: db, Log: &log}.Options(ctx)
	if err != nil {
		t.Fatal(err)
	}
	e := engine.New(db, reg, opts...)
	c, err := config.Load([]byte(fakeYAML))
	if err != nil {
		t.Fatal(err)
	}
	by := rollout.Identity{Kind: "human", Name: "felix"}
	r, err := e.Apply(ctx, engine.ApplyRequest{Config: c, Initiator: by})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := e.Rollback(ctx, r.ID, r.Desired, false); err != nil {
		t.Fatalf("rollback through booted engine: %v", err)
	}
	if _, _, err := e.Freeze(ctx, true, by, "incident-42"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Apply(ctx, engine.ApplyRequest{Config: c, Initiator: by}); err == nil {
		t.Fatal("apply after freeze through the one-shot engine must be refused")
	}
}
