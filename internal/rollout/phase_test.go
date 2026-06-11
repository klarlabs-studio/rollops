package rollout

import "testing"

func TestPhase_Settled(t *testing.T) {
	settled := []Phase{PhasePromoted, PhaseRolledBack}
	for _, p := range settled {
		if !p.Settled() {
			t.Errorf("%q must be settled", p)
		}
	}
	for _, p := range []Phase{PhasePending, PhaseValidating, PhaseAwaitingApproval, PhaseDeploying, PhasePaused, PhaseVerifying} {
		if p.Settled() {
			t.Errorf("%q must not be settled", p)
		}
	}
}

func TestPhase_Active(t *testing.T) {
	active := []Phase{PhasePending, PhaseValidating, PhaseDeploying, PhasePaused, PhaseVerifying}
	for _, p := range active {
		if !p.Active() {
			t.Errorf("%q must be active (in flight)", p)
		}
	}
	for _, p := range []Phase{PhasePromoted, PhaseRolledBack, PhaseAwaitingApproval} {
		if p.Active() {
			t.Errorf("%q must not be active", p)
		}
	}
}

func TestPhase_Degraded(t *testing.T) {
	if !PhaseRolledBack.Degraded() {
		t.Error("rolled-back must be degraded")
	}
	for _, p := range []Phase{PhasePromoted, PhaseDeploying, PhasePaused} {
		if p.Degraded() {
			t.Errorf("%q must not be degraded", p)
		}
	}
}
