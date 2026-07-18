package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"go.klarlabs.de/rollops/internal/engine"
	"go.klarlabs.de/rollops/internal/rollout"
)

// verifyOps serves a canned report so the CLI's rendering and exit behaviour
// can be pinned independently of the engine.
type verifyOps struct {
	statusNoteOps
	rep engine.VerifyReport
	err error
}

func (v verifyOps) Verify(context.Context, string) (engine.VerifyReport, error) {
	return v.rep, v.err
}

func TestCLI_VerifyPrintsEveryGateAndSucceeds(t *testing.T) {
	var buf bytes.Buffer
	app := &App{Out: &buf, Ops: verifyOps{rep: engine.VerifyReport{
		RolloutID: "ro-cli", Phase: string(rollout.PhaseVerifying), OK: true,
		Gates: []engine.GateResult{
			{Gate: engine.GateHealth, Status: engine.GatePass, Detail: "healthy"},
			{Gate: engine.GateSmoke, Status: engine.GatePass},
			{Gate: engine.GateAnalysis, Status: engine.GateSkipped, Detail: "no metric analysis configured"},
		},
	}}}
	if err := app.Run(context.Background(), []string{"verify", "ro-cli"}); err != nil {
		t.Fatalf("a passing verify should exit zero: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"ro-cli", "health\tpass\thealthy", "smoke\tpass", "analysis\tskipped", "nothing changed"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// TestCLI_VerifyFailingGateExitsNonZero pins the scripting contract:
// `rollops verify X && rollops promote X` must not promote past a failed gate.
func TestCLI_VerifyFailingGateExitsNonZero(t *testing.T) {
	var buf bytes.Buffer
	app := &App{Out: &buf, Ops: verifyOps{rep: engine.VerifyReport{
		RolloutID: "ro-cli", Phase: string(rollout.PhaseVerifying),
		OK:     false,
		Reason: "smoke test exit 1 (expected 0)",
		Gates: []engine.GateResult{
			{Gate: engine.GateHealth, Status: engine.GatePass, Detail: "healthy"},
			{Gate: engine.GateSmoke, Status: engine.GateFail, Detail: "smoke test exit 1 (expected 0)"},
			{Gate: engine.GateAnalysis, Status: engine.GateNotRun},
		},
	}}}
	err := app.Run(context.Background(), []string{"verify", "ro-cli"})
	if err == nil {
		t.Fatal("a failing gate must exit non-zero so `verify && promote` stops")
	}
	if !strings.Contains(err.Error(), "smoke test exit 1") {
		t.Errorf("error = %q, want the failing gate's reason", err)
	}
	out := buf.String()
	// The report still prints — the operator sees which gate failed and that the
	// later one never ran.
	if !strings.Contains(out, "smoke\tfail") || !strings.Contains(out, "analysis\tnot-run") {
		t.Errorf("output should show the full gate list:\n%s", out)
	}
	if strings.Contains(out, "nothing changed") {
		t.Errorf("failing verify should not print the ok line:\n%s", out)
	}
}

func TestCLI_VerifyRequiresRolloutID(t *testing.T) {
	var buf bytes.Buffer
	app := &App{Out: &buf, Ops: verifyOps{}}
	if err := app.Run(context.Background(), []string{"verify"}); err == nil {
		t.Fatal("verify without a rollout id should fail")
	}
}

func TestCLI_UsageListsVerify(t *testing.T) {
	var buf bytes.Buffer
	app := &App{Out: &buf, Ops: verifyOps{}}
	_ = app.Run(context.Background(), nil)
	if !strings.Contains(buf.String(), "verify <rollout-id>") {
		t.Errorf("usage should document verify:\n%s", buf.String())
	}
}
