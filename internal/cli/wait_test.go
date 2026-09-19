package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pt "go.klarlabs.de/rollops/pkg/target"
)

const waitCanaryYAML = `apiVersion: rollops.klarlabs.de/v1
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
    type: canary
    steps:
      - weight: 25
        pause: 2s
      - weight: 100
        pause: 2s
`

// Without the daemon, a canary with timed pauses stopped at "deploying" and
// nothing advanced it. --wait runs the daemon's own reconcile until it settles.
func TestCLI_ApplyWaitDrivesACanaryToPromotion(t *testing.T) {
	old := waitPoll
	waitPoll = time.Millisecond
	t.Cleanup(func() { waitPoll = old })

	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}}
	app, buf, _ := newAppWithTarget(t, fake, func() string { return "ro-wait" })
	cfg := filepath.Join(t.TempDir(), "canary.yaml")
	if err := os.WriteFile(cfg, []byte(waitCanaryYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := app.Run(context.Background(), []string{"apply", cfg, "--wait", "--wait-timeout", "1m"}); err != nil {
		t.Fatalf("apply --wait: %v\n%s", err, buf)
	}
	out := buf.String()
	if !strings.Contains(out, "rollout ro-wait: promoted") {
		t.Errorf("apply --wait did not finish the canary:\n%s", out)
	}
}

// A rollout that rolls back is a failure of `apply --wait`, not a success.
func TestCLI_ApplyWaitFailsWhenTheRolloutRollsBack(t *testing.T) {
	old := waitPoll
	waitPoll = time.Millisecond
	t.Cleanup(func() { waitPoll = old })

	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}}
	// A frozen clock: apply stops at the first step's bake, and the next tick's
	// bake-window health check is where it breaks.
	frozen := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	app, buf, _ := newAppWithClock(t, fake, func() string { return "ro-bad" }, func() time.Time { return frozen })
	cfg := filepath.Join(t.TempDir(), "canary.yaml")
	_ = os.WriteFile(cfg, []byte(waitCanaryYAML), 0o644)

	// Healthy for the first step's gate at apply time, then crashlooping by
	// the time --wait advances it.
	fake.failAfter = 1
	err := app.Run(context.Background(), []string{"apply", cfg, "--wait", "--wait-timeout", "30s"})
	if err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("a rolled-back rollout must fail --wait, got %v\n%s", err, buf)
	}
}
