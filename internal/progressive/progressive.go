// Package progressive sequences a deployment according to its strategy —
// rolling, canary, or blue-green — shifting traffic in configurable steps and
// gating each step on health. It is the orchestration layer above the target:
// each step performs the deploy action and, if health fails, aborts so the
// caller can roll back. The observability-free health gate (target Health /
// smoke test) is what advances or halts a step.
package progressive

import (
	"context"
	"fmt"
	"time"

	"go.klarlabs.de/rollops/internal/config"
)

// Step is one traffic-shifting increment.
type Step struct {
	Weight int           // target traffic percentage at this step (1..100)
	Pause  time.Duration // settle time before the health gate
}

// Plan is the resolved sequence of steps for a strategy.
type Plan struct {
	Strategy string
	Steps    []Step
}

// PlanFor resolves a config strategy into concrete steps. Canary uses the
// configured steps (defaulting to 10→50→100 if none); rolling ramps in quarters;
// blue-green is a single 100% cutover after the green stack is healthy.
func PlanFor(s config.Strategy) Plan {
	switch s.Type {
	case "canary":
		steps := fromConfig(s.Steps)
		if len(steps) == 0 {
			steps = []Step{{Weight: 10}, {Weight: 50}, {Weight: 100}}
		}
		steps = ensureFull(steps)
		return Plan{Strategy: "canary", Steps: steps}
	case "blue-green":
		return Plan{Strategy: "blue-green", Steps: []Step{{Weight: 100}}}
	default: // rolling
		return Plan{Strategy: "rolling", Steps: []Step{{Weight: 25}, {Weight: 50}, {Weight: 75}, {Weight: 100}}}
	}
}

func fromConfig(in []config.StrategyStep) []Step {
	out := make([]Step, 0, len(in))
	for _, s := range in {
		d, _ := time.ParseDuration(s.Pause)
		out = append(out, Step{Weight: s.Weight, Pause: d})
	}
	return out
}

// ensureFull guarantees the sequence ends at 100% so the rollout completes.
func ensureFull(steps []Step) []Step {
	if len(steps) == 0 || steps[len(steps)-1].Weight < 100 {
		return append(steps, Step{Weight: 100})
	}
	return steps
}

// Deployer performs the deploy action at a given traffic weight.
type Deployer func(ctx context.Context, weight int) error

// HealthGate reports whether the current step is healthy. A non-nil error aborts
// the rollout (the auto-rollback signal).
type HealthGate func(ctx context.Context) error

// Executor runs a Plan. Sleep is injectable so tests don't wait on real pauses.
type Executor struct {
	Deploy Deployer
	Health HealthGate
	Sleep  func(time.Duration)
}

// Run executes each step: deploy at the step weight, settle, then gate on
// health. The first failure aborts and is returned, naming the step.
func (e Executor) Run(ctx context.Context, p Plan) error {
	sleep := e.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	for i, step := range p.Steps {
		if err := e.Deploy(ctx, step.Weight); err != nil {
			return fmt.Errorf("progressive: %s step %d/%d (%d%%): deploy: %w", p.Strategy, i+1, len(p.Steps), step.Weight, err)
		}
		if step.Pause > 0 {
			sleep(step.Pause)
		}
		if e.Health != nil {
			if err := e.Health(ctx); err != nil {
				return fmt.Errorf("progressive: %s step %d/%d (%d%%): health gate failed: %w", p.Strategy, i+1, len(p.Steps), step.Weight, err)
			}
		}
	}
	return nil
}
