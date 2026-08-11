package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.klarlabs.de/rollops/internal/governance"
	"go.klarlabs.de/rollops/internal/rollout"
)

// Rollops decides what a deploy does; something else may decide whether it happens
// at all. The gate delegating that decision is only worth having if a refusal
// actually stops the deploy — a gate that logs its refusal and applies anyway is
// worse than none, because it reports governance that is not there. See ADR-012 and
// docs/external-governance.md.

// stubGovernor answers whatever it is told to, and records that it was asked.
type stubGovernor struct {
	decision governance.Decision
	err      error
	asked    []governance.Request
}

func (s *stubGovernor) Evaluate(_ context.Context, req governance.Request) (governance.Decision, error) {
	s.asked = append(s.asked, req)
	return s.decision, s.err
}

func TestApplyProceedsWhenGovernanceAllows(t *testing.T) {
	fake := &fakeTarget{}
	governor := &stubGovernor{decision: governance.Decision{Allowed: true}}
	e, _ := newEngine(t, fake, WithGovernance(governor))

	if _, err := e.Apply(context.Background(), ApplyRequest{
		Config:    loadConfig(t),
		Initiator: rollout.Identity{Kind: "human", Name: "felix"},
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(fake.applied) != 1 {
		t.Errorf("applied %d manifests, want 1: an allowing governor must not block", len(fake.applied))
	}
	if len(governor.asked) != 1 {
		t.Fatalf("the governor was asked %d times, want 1", len(governor.asked))
	}
}

// The point of the gate.
func TestApplyStopsWhenGovernanceRefuses(t *testing.T) {
	fake := &fakeTarget{}
	governor := &stubGovernor{decision: governance.Decision{
		Allowed: false,
		Reason:  "release 1.4.0 has no approval on record",
	}}
	e, _ := newEngine(t, fake, WithGovernance(governor))

	_, err := e.Apply(context.Background(), ApplyRequest{
		Config:    loadConfig(t),
		Initiator: rollout.Identity{Kind: "human", Name: "felix"},
	})
	if err == nil {
		t.Fatal("a refused apply must return an error")
	}
	if len(fake.applied) != 0 {
		t.Errorf("the target was written to %d times despite a refusal — the gate must stop "+
			"the deploy, not narrate it", len(fake.applied))
	}
	if !strings.Contains(err.Error(), "no approval on record") {
		t.Errorf("error = %q, want the governor's own reason so an operator knows what to fix", err)
	}
}

// A refusal is not an escalation. Escalating to approval would let an approver here
// overrule the system that was asked precisely because it knows something this
// engine does not.
func TestARefusalIsNotMerelyAnApprovalRequest(t *testing.T) {
	fake := &fakeTarget{}
	e, _ := newEngine(t, fake, WithGovernance(&stubGovernor{
		decision: governance.Decision{Allowed: false, Reason: "frozen"},
	}))

	r, err := e.Apply(context.Background(), ApplyRequest{Config: loadConfig(t)})
	if err == nil {
		t.Fatal("a refused apply must error")
	}
	if r != nil && r.Phase == rollout.PhaseAwaitingApproval {
		t.Error("a governance refusal became an approval request: an approver could then " +
			"click past a decision that was meant to be binding")
	}
}

// Fail closed. A configured governor that cannot be reached is not the same as no
// governor configured: the first is a failure, the second a choice. Giving them the
// same outcome would make the gate evaporate exactly when a rushed deploy is most
// likely — during an incident, on a bad network, mid-migration.
func TestApplyStopsWhenGovernanceIsUnreachable(t *testing.T) {
	fake := &fakeTarget{}
	e, _ := newEngine(t, fake, WithGovernance(&stubGovernor{
		err: errors.New("dial tcp: connection refused"),
	}))

	_, err := e.Apply(context.Background(), ApplyRequest{Config: loadConfig(t)})
	if err == nil {
		t.Fatal("an unreachable governor must block the apply")
	}
	if len(fake.applied) != 0 {
		t.Errorf("the target was written to %d times while governance was unreachable: the "+
			"gate must not be optional on a bad network", len(fake.applied))
	}
	if !strings.Contains(err.Error(), "unavailable") {
		t.Errorf("error = %q, want it to distinguish an outage from a refusal — reporting an "+
			"outage as a policy decision sends someone to read a policy that never ran", err)
	}
}

// An engine nobody configured for this must behave exactly as before. A zero Hook
// holds no provider and allows, so every existing deploy path is untouched.
func TestApplyIsUnaffectedWithoutAGovernor(t *testing.T) {
	fake := &fakeTarget{}
	e, _ := newEngine(t, fake)

	if _, err := e.Apply(context.Background(), ApplyRequest{Config: loadConfig(t)}); err != nil {
		t.Fatalf("Apply without governance configured: %v", err)
	}
	if len(fake.applied) != 1 {
		t.Errorf("applied %d manifests, want 1", len(fake.applied))
	}
}

// A governor cannot decide much from a target ref alone. The environment being
// deployed to and the version going there are the two facts it needs, and the
// rollout-time environment must win over the config's declared one — a config may be
// applied to more than one environment.
func TestTheGovernorIsToldWhatIsGoingWhere(t *testing.T) {
	fake := &fakeTarget{}
	governor := &stubGovernor{decision: governance.Decision{Allowed: true}}
	e, _ := newEngine(t, fake, WithGovernance(governor))

	cfg := loadConfig(t)
	if cfg.Metadata.Labels == nil {
		cfg.Metadata.Labels = map[string]string{}
	}
	cfg.Metadata.Labels[LabelVersion] = "1.4.0"

	if _, err := e.Apply(context.Background(), ApplyRequest{
		Config:    cfg,
		Initiator: rollout.Identity{Kind: "ci", Name: "pipeline-7"},
		Risk:      RiskInputs{Environment: "prod"},
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if len(governor.asked) != 1 {
		t.Fatalf("the governor was asked %d times, want 1", len(governor.asked))
	}
	asked := governor.asked[0]
	if asked.Action != "apply" {
		t.Errorf("Action = %q, want apply", asked.Action)
	}
	if asked.Environment != "prod" {
		t.Errorf("Environment = %q, want prod from the rollout-time input", asked.Environment)
	}
	if asked.Version != "1.4.0" {
		t.Errorf("Version = %q, want 1.4.0 — without it an external system holding a "+
			"release record cannot find the release being deployed", asked.Version)
	}
	if asked.Actor.Name != "pipeline-7" {
		t.Errorf("Actor.Name = %q, want pipeline-7", asked.Actor.Name)
	}
	if asked.TargetRef == "" {
		t.Error("TargetRef must be set: it is the only field identifying the destination")
	}
}
