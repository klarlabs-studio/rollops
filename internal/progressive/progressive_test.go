package progressive

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.klarlabs.de/rollops/internal/config"
)

func TestPlanFor_Canary_DefaultsAndEndsAt100(t *testing.T) {
	p := PlanFor(config.Strategy{Type: "canary"})
	if p.Steps[len(p.Steps)-1].Weight != 100 {
		t.Errorf("canary must end at 100%%, got %v", p.Steps)
	}
}

func TestPlanFor_Canary_UsesConfiguredSteps(t *testing.T) {
	p := PlanFor(config.Strategy{Type: "canary", Steps: []config.StrategyStep{{Weight: 5, Pause: "1s"}, {Weight: 100}}})
	if len(p.Steps) != 2 || p.Steps[0].Weight != 5 || p.Steps[0].Pause != time.Second {
		t.Errorf("configured steps not used: %v", p.Steps)
	}
}

func TestPlanFor_BlueGreen_SingleCutover(t *testing.T) {
	p := PlanFor(config.Strategy{Type: "blue-green"})
	if len(p.Steps) != 1 || p.Steps[0].Weight != 100 {
		t.Errorf("blue-green should be single 100%% cutover, got %v", p.Steps)
	}
}

func TestPlanFor_Rolling_Ramps(t *testing.T) {
	p := PlanFor(config.Strategy{Type: "rolling"})
	if len(p.Steps) != 4 || p.Steps[3].Weight != 100 {
		t.Errorf("rolling should ramp to 100, got %v", p.Steps)
	}
}

func TestExecutor_RunsAllStepsInOrder(t *testing.T) {
	var weights []int
	exec := Executor{
		Deploy: func(_ context.Context, w int) error { weights = append(weights, w); return nil },
		Health: func(context.Context) error { return nil },
		Sleep:  func(time.Duration) {},
	}
	if err := exec.Run(context.Background(), PlanFor(config.Strategy{Type: "canary", Steps: []config.StrategyStep{{Weight: 10}, {Weight: 50}}})); err != nil {
		t.Fatal(err)
	}
	want := []int{10, 50, 100}
	if len(weights) != 3 || weights[0] != 10 || weights[2] != 100 {
		t.Errorf("weights = %v, want %v", weights, want)
	}
}

func TestExecutor_HealthFailureAborts(t *testing.T) {
	var deploys int
	exec := Executor{
		Deploy: func(context.Context, int) error { deploys++; return nil },
		Health: func(context.Context) error {
			if deploys >= 1 {
				return errors.New("503")
			}
			return nil
		},
		Sleep: func(time.Duration) {},
	}
	err := exec.Run(context.Background(), PlanFor(config.Strategy{Type: "rolling"}))
	if err == nil {
		t.Fatal("expected abort on health failure")
	}
	if deploys != 1 {
		t.Errorf("should abort after first failing step; deploys=%d", deploys)
	}
}

func TestExecutor_DeployFailureAborts(t *testing.T) {
	exec := Executor{
		Deploy: func(context.Context, int) error { return errors.New("connection refused") },
		Sleep:  func(time.Duration) {},
	}
	if err := exec.Run(context.Background(), PlanFor(config.Strategy{Type: "blue-green"})); err == nil {
		t.Fatal("expected abort on deploy failure")
	}
}
