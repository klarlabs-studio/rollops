package boot

import (
	"bytes"
	"context"
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
	reg.Register("fake", func(config.Target) (pt.Target, error) { return fake, nil })

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
