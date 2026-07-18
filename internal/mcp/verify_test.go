package mcp

import (
	"testing"
)

// TestTools_VerifyReportsGatesWithoutPromoting is the agent-facing contract: an
// agent can ask "would this promote?" and get a per-gate answer, with the
// rollout left exactly where it was.
func TestTools_VerifyReportsGatesWithoutPromoting(t *testing.T) {
	tl := newTools(t)
	ctx := asAgent("nomi")
	if _, err := tl.Apply(ctx, ApplyInput{Config: cfgYAML}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	out, err := tl.Verify(ctx, ActionInput{RolloutID: "ro-mcp-1"})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !out.OK {
		t.Errorf("healthy target should verify: %+v", out.Gates)
	}
	if out.RolloutID != "ro-mcp-1" {
		t.Errorf("rollout_id = %q, want ro-mcp-1", out.RolloutID)
	}
	if len(out.Gates) != 3 {
		t.Errorf("got %d gates, want all 3 reported: %+v", len(out.Gates), out.Gates)
	}
	if out.Phase != "verifying" {
		t.Errorf("phase = %q, want verifying — reported, not advanced", out.Phase)
	}

	// Still promotable afterwards: the dry run consumed nothing.
	st, err := tl.Status(ctx, StatusInput{RolloutID: "ro-mcp-1"})
	if err != nil {
		t.Fatal(err)
	}
	if st.Phase != "verifying" {
		t.Errorf("phase after a dry run = %q, want it untouched", st.Phase)
	}
}

func TestTools_VerifyUnknownRollout(t *testing.T) {
	tl := newTools(t)
	if _, err := tl.Verify(asAgent("nomi"), ActionInput{RolloutID: "nope"}); err == nil {
		t.Fatal("verifying an unknown rollout should error")
	}
}
