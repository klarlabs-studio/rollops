package engine

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/rollops/internal/audit"
	"go.klarlabs.de/rollops/internal/config"
	"go.klarlabs.de/rollops/internal/notify"
	"go.klarlabs.de/rollops/internal/rollout"
	"go.klarlabs.de/rollops/internal/secrets"
	"go.klarlabs.de/rollops/internal/security"
	"go.klarlabs.de/rollops/internal/store/sqlite"
	itarget "go.klarlabs.de/rollops/internal/target"
	pt "go.klarlabs.de/rollops/pkg/target"
)

const pipeYAML = `
apiVersion: rollops.klarlabs.de/v1
kind: RolloutConfig
metadata:
  name: pay
spec:
  target:
    kind: fake
    ref: pay/prod/api
    criticality: high
    spec:
      image: ghcr.io/acme/api:1.2.3
      token: "secret:acme/prod/api.token"
  strategy:
    type: rolling
`

// captureTarget records the spec it was built with so secret resolution can be
// asserted.
type captureTarget struct {
	spec    map[string]any
	applied int
}

func (c *captureTarget) Apply(context.Context, pt.Manifest) (pt.Result, error) {
	c.applied++
	return pt.Result{Changed: true}, nil
}
func (c *captureTarget) Observe(context.Context) (pt.Fingerprint, error) {
	return pt.Fingerprint{}, nil
}
func (c *captureTarget) Health(context.Context) (pt.HealthStatus, error) {
	return pt.HealthStatus{State: pt.HealthHealthy}, nil
}

type fakeVerifier struct{ ok bool }

func (f fakeVerifier) Verify(context.Context, string) (bool, string, error) {
	return f.ok, "stub", nil
}

func wiredEngine(t *testing.T, opts ...Option) (*Engine, *captureTarget, *bytes.Buffer) {
	t.Helper()
	db, err := sqlite.Open(t.TempDir() + "/p.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	cap := &captureTarget{}
	reg := itarget.NewRegistry()
	reg.Register("fake", func(c config.Target) (pt.Target, error) { cap.spec = c.Spec; return cap, nil })
	var buf bytes.Buffer
	base := []Option{
		WithClock(func() time.Time { return time.Unix(0, 0) }),
		WithIDGen(func() string { return "ro-pipe" }),
		WithAudit(audit.New(&buf)),
	}
	return New(db, reg, append(base, opts...)...), cap, &buf
}

func loadPipe(t *testing.T) *config.Config {
	t.Helper()
	c, err := config.Load([]byte(pipeYAML))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestPipeline_FreezeBlocks(t *testing.T) {
	g := &security.Guardrails{Freeze: security.NewFreeze(), Floor: security.DefaultPolicyFloor()}
	g.Freeze.Engage(rollout.Identity{Kind: "human", Name: "felix"}, "incident")
	e, cap, buf := wiredEngine(t, WithGuardrails(g))

	_, err := e.Apply(context.Background(), ApplyRequest{Config: loadPipe(t)})
	if err != security.ErrFrozen {
		t.Fatalf("err = %v, want ErrFrozen", err)
	}
	if cap.applied != 0 {
		t.Error("frozen rollout must not touch the target")
	}
	if !strings.Contains(buf.String(), "blocked") {
		t.Errorf("freeze should be audited: %s", buf.String())
	}
}

func TestPipeline_ArtifactRejected(t *testing.T) {
	e, cap, _ := wiredEngine(t, WithArtifactGate(security.ArtifactGate{
		Mode: security.VerifyEnforce, Verifier: fakeVerifier{ok: false},
	}))
	_, err := e.Apply(context.Background(), ApplyRequest{Config: loadPipe(t)})
	if err == nil {
		t.Fatal("unverified artifact must block deploy")
	}
	if cap.applied != 0 {
		t.Error("target must not be touched when artifact is rejected")
	}
}

func TestPipeline_ArtifactVerifiedProceeds(t *testing.T) {
	e, cap, _ := wiredEngine(t, WithArtifactGate(security.ArtifactGate{
		Mode: security.VerifyEnforce, Verifier: fakeVerifier{ok: true},
	}))
	if _, err := e.Apply(context.Background(), ApplyRequest{Config: loadPipe(t)}); err != nil {
		t.Fatalf("verified artifact should deploy: %v", err)
	}
	if cap.applied != 1 {
		t.Errorf("verified artifact should deploy once, applied=%d", cap.applied)
	}
}

func TestPipeline_SecretResolvedIntoTarget(t *testing.T) {
	t.Setenv("ACME_PROD_API_TOKEN", "real-token-value")
	e, cap, buf := wiredEngine(t, WithSecrets(secrets.EnvProvider{}))

	if _, err := e.Apply(context.Background(), ApplyRequest{Config: loadPipe(t)}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if cap.spec["token"] != "real-token-value" {
		t.Errorf("secret not resolved into target spec: %v", cap.spec["token"])
	}
	if strings.Contains(buf.String(), "real-token-value") {
		t.Error("resolved secret leaked into the audit trail")
	}
}

type captureNotifier struct{ events []notify.Event }

func (c *captureNotifier) Notify(_ context.Context, e notify.Event) error {
	c.events = append(c.events, e)
	return nil
}
func (c *captureNotifier) kinds() []notify.Kind {
	out := make([]notify.Kind, 0, len(c.events))
	for _, e := range c.events {
		out = append(out, e.Kind)
	}
	return out
}

func TestPipeline_NotifiesApprovalAndPromotion(t *testing.T) {
	n := &captureNotifier{}
	e, _, _ := wiredEngine(t, WithNotifier(n))
	ctx := context.Background()

	// Gated apply → approval-needed notification.
	r, err := e.Apply(ctx, ApplyRequest{Config: loadPipe(t), NeedsApproval: true})
	if err != nil {
		t.Fatal(err)
	}
	if r.Phase != rollout.PhaseAwaitingApproval {
		t.Fatalf("phase = %q", r.Phase)
	}
	// Approve then finalize → promotion notification.
	if _, err := e.Approve(ctx, r.ID, rollout.Identity{Kind: "human", Name: "felix"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.VerifyOrRollback(ctx, r.ID, r.Desired, loadPipe(t)); err != nil {
		t.Fatal(err)
	}
	got := n.kinds()
	if !contains(got, notify.ApprovalNeeded) || !contains(got, notify.Promoted) {
		t.Errorf("notifications = %v, want approval_needed + promoted", got)
	}
}

func contains(ks []notify.Kind, want notify.Kind) bool {
	for _, k := range ks {
		if k == want {
			return true
		}
	}
	return false
}

const analysisYAML = `
apiVersion: rollops.klarlabs.de/v1
kind: RolloutConfig
metadata:
  name: analyzed
spec:
  target:
    kind: fake
    ref: analyzed/prod/api
    criticality: low
    spec:
      x: 1
  strategy:
    type: rolling
  rollback:
    auto: true
  analysis:
    provider: prometheus
    address: http://prom
    metrics:
      - {name: errorRate, query: "rate(err[1m])"}
    condition: "errorRate < 0.05"
    count: 2
    failureLimit: 1
`

type fixedMetrics float64

func (f fixedMetrics) Query(context.Context, string) (float64, error) { return float64(f), nil }

func TestPipeline_MetricAnalysisRollsBack(t *testing.T) {
	c, err := config.Load([]byte(analysisYAML))
	if err != nil {
		t.Fatal(err)
	}
	e, _, _ := wiredEngine(t, WithMetricsProvider(fixedMetrics(0.2))) // error rate breaches 0.05
	ctx := context.Background()
	r, err := e.Apply(ctx, ApplyRequest{Config: c})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	out, err := e.VerifyOrRollback(ctx, r.ID, pt.Manifest{Kind: "fake", Checksum: "prior"}, c)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !out.RolledBack {
		t.Fatal("breaching metric analysis should auto-roll-back")
	}
	if !strings.Contains(out.Reason, "analysis") {
		t.Errorf("reason = %q, want analysis failure", out.Reason)
	}
	hist, err := e.History(ctx, "analyzed/prod/api")
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) == 0 || !strings.Contains(hist[0].Note, "analysis failed") {
		t.Fatalf("history note = %+v, want analysis failure note", hist)
	}
}

func TestPipeline_MetricAnalysisDisabledByDefault(t *testing.T) {
	c, err := config.Load([]byte(analysisYAML))
	if err != nil {
		t.Fatal(err)
	}
	e, _, _ := wiredEngine(t)
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
		t.Fatal("metric analysis must be disabled by default for observability-free v1 rollback")
	}
}

func TestPipeline_MetricAnalysisPromotes(t *testing.T) {
	c, _ := config.Load([]byte(analysisYAML))
	e, _, _ := wiredEngine(t, WithMetricsProvider(fixedMetrics(0.01))) // healthy
	ctx := context.Background()
	r, _ := e.Apply(ctx, ApplyRequest{Config: c})
	out, err := e.VerifyOrRollback(ctx, r.ID, pt.Manifest{Kind: "fake", Checksum: "prior"}, c)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if out.RolledBack {
		t.Fatalf("healthy metrics should promote; reason=%q", out.Reason)
	}
	hist, err := e.History(ctx, "analyzed/prod/api")
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) == 0 || !strings.Contains(hist[0].Note, "analysis passed") {
		t.Fatalf("history note = %+v, want analysis pass note", hist)
	}
}

func TestPipeline_PolicyFloorForcesApproval(t *testing.T) {
	// Prod schema change: the non-bypassable floor forces approval even with no
	// risk block configured.
	e, cap, _ := wiredEngine(t, WithGuardrails(&security.Guardrails{Floor: security.DefaultPolicyFloor()}))
	r, err := e.Apply(context.Background(), ApplyRequest{
		Config: loadPipe(t),
		Risk:   RiskInputs{Environment: "prod", ChangeType: "schema"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.Phase != rollout.PhaseAwaitingApproval {
		t.Errorf("prod schema must halt at approval (policy floor); phase=%q", r.Phase)
	}
	if cap.applied != 0 {
		t.Error("floored rollout must not deploy")
	}
}
