package governance

import (
	"context"
	"testing"

	"go.klarlabs.de/rollops/internal/rollout"
)

type fakeGov struct {
	req Request
	out Decision
}

func (f *fakeGov) Evaluate(_ context.Context, req Request) (Decision, error) {
	f.req = req
	return f.out, nil
}

func TestHookEvaluate(t *testing.T) {
	p := &fakeGov{out: Decision{Allowed: true, Evidence: map[string]string{"ticket": "REL-1"}}}
	h := Hook{Provider: p}
	req := Request{Action: "apply", TargetRef: "svc/prod/api", Actor: rollout.Identity{Kind: "human", Name: "felix"}}
	d, err := h.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Allowed || d.Evidence["ticket"] != "REL-1" || p.req.TargetRef != req.TargetRef {
		t.Fatalf("decision=%+v providerReq=%+v", d, p.req)
	}
}

func TestHookEvaluateValidationAndDefaults(t *testing.T) {
	if d, err := (Hook{}).Evaluate(context.Background(), Request{}); err != nil || !d.Allowed {
		t.Fatalf("nil provider should allow: d=%+v err=%v", d, err)
	}
	d, err := (Hook{Provider: &fakeGov{out: Decision{Allowed: false}}}).Evaluate(context.Background(), Request{Action: "apply", TargetRef: "svc"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Allowed || d.Reason == "" {
		t.Fatalf("denial should carry reason: %+v", d)
	}
	if _, err := (Hook{Provider: &fakeGov{}}).Evaluate(context.Background(), Request{}); err == nil {
		t.Fatal("missing action/target should fail")
	}
}
