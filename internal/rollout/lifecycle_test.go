package rollout

import "testing"

func TestLifecycle_HappyPath_NoApproval(t *testing.T) {
	l, err := NewLifecycle(LifeContext{PlanProduced: true, NeedsApproval: false})
	if err != nil {
		t.Fatal(err)
	}
	if l.Phase() != PhasePending {
		t.Fatalf("initial = %q", l.Phase())
	}
	steps := []struct {
		event string
		want  Phase
	}{
		{EventValidate, PhaseValidating},
		{EventGate, PhaseDeploying},
		{EventDeployed, PhaseVerifying},
		{EventVerifyOK, PhasePromoted},
	}
	for _, s := range steps {
		got, err := l.Send(s.event)
		if err != nil {
			t.Fatalf("Send(%s): %v", s.event, err)
		}
		if got != s.want {
			t.Fatalf("after %s phase = %q, want %q", s.event, got, s.want)
		}
	}
}

func TestLifecycle_ApprovalBranch(t *testing.T) {
	l, _ := NewLifecycle(LifeContext{PlanProduced: true, NeedsApproval: true})
	_, _ = l.Send(EventValidate)
	got, err := l.Send(EventGate)
	if err != nil {
		t.Fatal(err)
	}
	if got != PhaseAwaitingApproval {
		t.Fatalf("gate with NeedsApproval -> %q, want awaiting-approval", got)
	}
	got, err = l.Send(EventApprove)
	if err != nil {
		t.Fatal(err)
	}
	if got != PhaseDeploying {
		t.Fatalf("approve -> %q, want deploying", got)
	}
}

func TestLifecycle_RejectRollsBack(t *testing.T) {
	l, _ := NewLifecycle(LifeContext{PlanProduced: true, NeedsApproval: true})
	_, _ = l.Send(EventValidate)
	_, _ = l.Send(EventGate)
	got, err := l.Send(EventReject)
	if err != nil {
		t.Fatal(err)
	}
	if got != PhaseRolledBack {
		t.Fatalf("reject -> %q, want rolled-back", got)
	}
}

// Agent-driven apply requires a plan: without PlanProduced the GATE guard to
// deploying must not pass.
func TestLifecycle_NoPlan_CannotDeploy(t *testing.T) {
	l, _ := NewLifecycle(LifeContext{PlanProduced: false, NeedsApproval: false})
	_, _ = l.Send(EventValidate)
	_, err := l.Send(EventGate)
	if err == nil {
		t.Fatal("expected GATE to be rejected without a produced plan")
	}
	if l.Phase() != PhaseValidating {
		t.Fatalf("phase = %q, want still validating", l.Phase())
	}
}

func TestLifecycle_IllegalTransition(t *testing.T) {
	l, _ := NewLifecycle(LifeContext{PlanProduced: true})
	// VERIFY_OK is not valid from pending.
	if _, err := l.Send(EventVerifyOK); err == nil {
		t.Fatal("expected illegal-transition error")
	}
}

func TestLifecycle_RollbackFromDeploying(t *testing.T) {
	l, _ := NewLifecycle(LifeContext{PlanProduced: true})
	_, _ = l.Send(EventValidate)
	_, _ = l.Send(EventGate)
	got, err := l.Send(EventRollback)
	if err != nil {
		t.Fatal(err)
	}
	if got != PhaseRolledBack {
		t.Fatalf("rollback from deploying -> %q", got)
	}
}

func TestLifecycle_VerifyFailRollback(t *testing.T) {
	l, _ := NewLifecycle(LifeContext{PlanProduced: true})
	_, _ = l.Send(EventValidate)
	_, _ = l.Send(EventGate)
	_, _ = l.Send(EventDeployed)
	got, err := l.Send(EventRollback)
	if err != nil {
		t.Fatal(err)
	}
	if got != PhaseRolledBack {
		t.Fatalf("rollback from verifying -> %q", got)
	}
}

func TestResumeLifecycle_MidFlight(t *testing.T) {
	l, err := ResumeLifecycle(PhaseDeploying, LifeContext{PlanProduced: true})
	if err != nil {
		t.Fatal(err)
	}
	if l.Phase() != PhaseDeploying {
		t.Fatalf("resumed phase = %q", l.Phase())
	}
	got, err := l.Send(EventDeployed)
	if err != nil {
		t.Fatal(err)
	}
	if got != PhaseVerifying {
		t.Fatalf("resume then deployed -> %q", got)
	}
}
