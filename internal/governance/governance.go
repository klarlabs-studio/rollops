package governance

import (
	"context"
	"fmt"

	"go.klarlabs.de/rollops/internal/rollout"
)

type Request struct {
	Action    string
	TargetRef string
	Actor     rollout.Identity

	// Environment and Version say what is going where.
	//
	// A governor cannot decide much from a target reference alone: "may this proceed"
	// has a different answer for staging than for production, and an external system
	// holding a release record needs the version to find it. Both are optional, so a
	// caller that does not know them can still ask.
	Environment string
	Version     string
}

type Decision struct {
	Allowed  bool
	Reason   string
	Evidence map[string]string
}

type Provider interface {
	Evaluate(ctx context.Context, req Request) (Decision, error)
}

type Hook struct {
	Provider Provider
}

func (h Hook) Evaluate(ctx context.Context, req Request) (Decision, error) {
	if h.Provider == nil {
		return Decision{Allowed: true}, nil
	}
	if req.Action == "" || req.TargetRef == "" {
		return Decision{}, fmt.Errorf("governance: action and target are required")
	}
	d, err := h.Provider.Evaluate(ctx, req)
	if err != nil {
		return Decision{}, err
	}
	if !d.Allowed && d.Reason == "" {
		d.Reason = "denied by governance hook"
	}
	return d, nil
}
