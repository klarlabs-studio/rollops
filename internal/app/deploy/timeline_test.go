package deploy_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"go.klarlabs.de/rollops/internal/app/deploy"
	"go.klarlabs.de/rollops/internal/app/port"
	"go.klarlabs.de/rollops/internal/domain/deployment"
	"go.klarlabs.de/rollops/internal/domain/event"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/policy"
)

func (h *harness) timeline(t *testing.T) []event.Event {
	t.Helper()
	es, err := h.store.Events().Timeline(context.Background(), port.Page{})
	if err != nil {
		t.Fatalf("read timeline: %v", err)
	}
	return es
}

func types(es []event.Event) []event.Type {
	out := make([]event.Type, len(es))
	for i, e := range es {
		out[i] = e.Type
	}
	return out
}

func find(t *testing.T, es []event.Event, want event.Type) event.Event {
	t.Helper()
	for _, e := range es {
		if e.Type == want {
			return e
		}
	}
	t.Fatalf("no %s in the timeline, only %v", want, types(es))
	return event.Event{}
}

func decode(t *testing.T, e event.Event) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(e.Payload, &out); err != nil {
		t.Fatalf("decode %s payload: %v", e.Type, err)
	}
	return out
}

func TestPlanningRecordsWhatWasPlannedAndWhatPolicySaid(t *testing.T) {
	h := newHarness(t)
	p := h.plan(t)

	es := h.timeline(t)
	if got := types(es); len(got) != 2 ||
		got[0] != event.DeploymentPlanCreated || got[1] != event.PolicyEvaluated {
		t.Fatalf("planning wrote %v, want [%s %s]",
			got, event.DeploymentPlanCreated, event.PolicyEvaluated)
	}

	created, evaluated := es[0], es[1]
	if created.AggregateType != event.AggregatePlan || created.AggregateID != string(p.ID) {
		t.Errorf("the created event is filed against %s/%s, want plan/%s",
			created.AggregateType, created.AggregateID, p.ID)
	}
	// The first event of an intent starts the chain: it correlates to itself
	// and nothing caused it.
	if created.CorrelationID != created.ID {
		t.Errorf("the root correlates to %s, want its own id %s", created.CorrelationID, created.ID)
	}
	if created.CausationID != "" {
		t.Errorf("the root claims %s caused it", created.CausationID)
	}
	if evaluated.CorrelationID != created.CorrelationID {
		t.Errorf("policy.evaluated correlates to %s, want %s",
			evaluated.CorrelationID, created.CorrelationID)
	}
	if evaluated.CausationID != created.ID {
		t.Errorf("policy.evaluated was caused by %s, want %s", evaluated.CausationID, created.ID)
	}

	payload := decode(t, created)
	for field, want := range map[string]any{
		"environment_id": string(h.env.ID),
		"release_id":     string(h.release.ID),
		"strategy":       string(deployment.StrategyCanary),
		"hash":           p.Hash.String(),
	} {
		if got := payload[field]; got != want {
			t.Errorf("payload %s is %v, want %v", field, got, want)
		}
	}
	if got := payload["operations"]; got != float64(len(p.Operations)) {
		t.Errorf("payload operations is %v, want %d", got, len(p.Operations))
	}

	verdict := decode(t, evaluated)
	if verdict["allowed"] != true {
		t.Errorf("policy.evaluated says allowed=%v, want true", verdict["allowed"])
	}
	if verdict["risk_level"] != string(policy.RiskMedium) {
		t.Errorf("policy.evaluated says risk %v, want %s", verdict["risk_level"], policy.RiskMedium)
	}
	// Factors carry codes rather than the messages written for a person. A
	// code is what a query matches on, and a message is prose that a future
	// wording change would silently rewrite the history of.
	factors, _ := verdict["factors"].([]any)
	if len(factors) != 1 || factors[0] != "production_environment" {
		t.Errorf("policy.evaluated lists factors %v, want [production_environment]", factors)
	}
}

func TestPlanningAndApplyingAreOneIntent(t *testing.T) {
	h := newHarness(t)
	p := h.plan(t)
	d := h.apply(t, p)

	es := h.timeline(t)
	root := es[0]
	queued := find(t, es, event.DeploymentQueued)

	if queued.AggregateType != event.AggregateDeployment || queued.AggregateID != string(d.ID) {
		t.Errorf("the admission is filed against %s/%s, want deployment/%s",
			queued.AggregateType, queued.AggregateID, d.ID)
	}
	// §16.4: one user intent, one correlation id, across plan and apply — even
	// though they are separate calls that may be hours and processes apart.
	if queued.CorrelationID != root.CorrelationID {
		t.Errorf("the admission correlates to %s, want the plan's %s",
			queued.CorrelationID, root.CorrelationID)
	}
	if queued.CausationID != root.ID {
		t.Errorf("the admission was caused by %s, want %s", queued.CausationID, root.ID)
	}

	chain, err := h.store.Events().ForCorrelation(context.Background(), root.CorrelationID, port.Page{})
	if err != nil {
		t.Fatalf("read the chain: %v", err)
	}
	if len(chain) != 3 {
		t.Errorf("the intent spans %d events, want 3: %v", len(chain), types(chain))
	}

	payload := decode(t, queued)
	if payload["plan_id"] != string(p.ID) {
		t.Errorf("the admission names plan %v, want %s", payload["plan_id"], p.ID)
	}
	if payload["trigger"] != string(deployment.TriggerManual) {
		t.Errorf("the admission names trigger %v, want %s", payload["trigger"], deployment.TriggerManual)
	}
}

func TestAnApplyThatNeedsApprovalSaysSoRatherThanQueueing(t *testing.T) {
	h := newHarness(t)
	decision := allowed()
	decision.Requirements = []policy.Requirement{
		{Type: policy.RequireApproval, Role: "sre", Count: 2},
	}
	h.policy.decision = decision

	p := h.plan(t)
	h.apply(t, p)

	es := h.timeline(t)
	for _, e := range es {
		if e.Type == event.DeploymentQueued {
			t.Fatalf("a deployment waiting on approval was recorded as queued")
		}
	}
	requested := find(t, es, event.DeploymentApprovalRequested)
	payload := decode(t, requested)
	reqs, _ := payload["requirements"].([]any)
	if len(reqs) != 1 || reqs[0] != string(policy.RequireApproval) {
		t.Errorf("the request lists requirements %v, want [approval]", reqs)
	}
}

func TestARefusedApplyRecordsNothing(t *testing.T) {
	h := newHarness(t)
	p := h.plan(t)
	h.apply(t, p)
	before := len(h.timeline(t))

	// A second apply finds the environment busy. The refusal happens inside
	// the same transaction as the write it would otherwise make, so it must
	// leave the timeline exactly as it found it.
	_, err := h.service.Apply(context.Background(), deploy.ApplyCommand{
		PlanID:  p.ID,
		Trigger: deployment.Trigger{Type: deployment.TriggerManual},
		Actor:   actor(),
	})
	if !errors.Is(err, deploy.ErrEnvironmentBusy) {
		t.Fatalf("the second apply gave %v, want ErrEnvironmentBusy", err)
	}
	if after := len(h.timeline(t)); after != before {
		t.Errorf("the refused apply left %d events behind", after-before)
	}
}

func TestTheTimelineIsAttributedAndCarriesNoSecret(t *testing.T) {
	h := newHarness(t)
	h.apply(t, h.plan(t))

	for _, e := range h.timeline(t) {
		if e.Principal.ID != actor().ID {
			t.Errorf("%s is attributed to %q, want %q", e.Type, e.Principal.ID, actor().ID)
		}
		if e.Principal.Type != identity.PrincipalHuman {
			t.Errorf("%s is attributed to a %s", e.Type, e.Principal.Type)
		}
		// INV-012: the actor carries a token claim, and no route from a command
		// to a row that cannot be rewritten may take it along.
		if strings.Contains(e.Principal.Claims["token"], "s3cret") {
			t.Errorf("%s carries the actor's token", e.Type)
		}
		if strings.Contains(string(e.Payload), "s3cret") {
			t.Errorf("%s has a token in its payload", e.Type)
		}
	}
}
