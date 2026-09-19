// Package risk estimates how dangerous a deployment plan is, and says why.
//
// It authorizes nothing. Assess returns a policy.RiskAssessment, which is one
// field of a policy.Decision — an input the engine weighs alongside everything
// else rather than a parallel gate that could refuse on its own (spec 12.4).
// Nothing here can stop a deployment; it can only describe one.
//
// The estimate is derived from the plan itself — where it lands, how it rolls
// out, how much it touches, and whether any of it can be undone. There is no
// model and no history: every point of the score is attributable to a signal
// the reviewer can see in the plan they are reading, which is what spec 12.4's
// "never let an opaque score become the sole authorization mechanism" asks for
// at the level a scorer can honour.
package risk

import (
	"fmt"

	"go.klarlabs.de/rollops/internal/domain/deployment"
	"go.klarlabs.de/rollops/internal/domain/environment"
	"go.klarlabs.de/rollops/internal/domain/plan"
	"go.klarlabs.de/rollops/internal/domain/policy"
)

// Factor codes are stable so that a surface — or a policy expression — can pick
// one out of an assessment without matching on prose.
const (
	FactorEnvironment  = "environment"
	FactorIrreversible = "irreversible"
	FactorSensitive    = "sensitive_change"
	FactorBlastRadius  = "blast_radius"
	FactorStrategy     = "strategy"
)

// Signals are what the estimate is derived from. They are the plan's own
// fields rather than observability data: an assessment has to be reproducible
// from the stored plan long after the metrics that would have informed it have
// rolled over.
type Signals struct {
	Environment environment.Kind
	Strategy    deployment.Strategy
	Operations  []plan.PlannedOperation
}

// Weights are each signal's share of the score. They are stated here rather
// than configured because the score's only job is to band a plan for policy,
// and a threshold an operator can move is the knob that belongs to them.
//
// Environment leads because it decides who notices. Reversibility comes next:
// every other risk can be answered by deploying again, and that one cannot.
const (
	weightEnvironment  = 0.30
	weightIrreversible = 0.25
	weightSensitive    = 0.15
	weightBlastRadius  = 0.15
	weightStrategy     = 0.15
)

// wideChange is the operation count treated as the largest blast radius there
// is. Past it the signal saturates: the difference between forty operations
// and four hundred is not something a reviewer can act on differently.
const wideChange = 10

// environmentRisk scores where the change lands.
//
// An unrecognised kind — including "custom", which is the operator declining to
// say — scores as production. Reading silence as harmless is how an unlabelled
// production environment slips past the gate that kinds exist to enforce.
func environmentRisk(k environment.Kind) (float64, string) {
	switch k {
	case environment.KindDevelopment:
		return 0, "development environment"
	case environment.KindPreview:
		return 0.25, "preview environment"
	case environment.KindStaging:
		return 0.5, "staging environment"
	case environment.KindProduction:
		return 1, "production environment"
	default:
		return 1, fmt.Sprintf("environment kind %q does not say what is at stake; treated as production", k)
	}
}

// strategyRisk scores how much moves at once. Canary exposes the change to a
// slice before anyone commits to it; recreate takes everything down and brings
// it back. An unrecognised strategy scores as the worst one for the same reason
// an unrecognised environment does.
func strategyRisk(s deployment.Strategy) (float64, string) {
	switch s {
	case deployment.StrategyCanary:
		return 0, "canary rollout"
	case deployment.StrategyRolling:
		return 0.5, "rolling update replaces instances in place"
	case deployment.StrategyBlueGreen:
		return 0.75, "blue/green cuts every request over at once"
	case deployment.StrategyRecreate:
		return 1, "recreate takes the release down before bringing it back"
	default:
		return 1, fmt.Sprintf("strategy %q is not recognised; treated as a full cutover", s)
	}
}

func clamp01(v float64) float64 {
	switch {
	case v < 0:
		return 0
	case v > 1:
		return 1
	default:
		return v
	}
}

// level bands a score. The bands are quartiles: the number is the authority and
// the band is how policy talks about it, which is why a policy is written
// against the band and an operator's threshold against the number.
func level(score float64) policy.RiskLevel {
	switch {
	case score < 0.25:
		return policy.RiskLow
	case score < 0.5:
		return policy.RiskMedium
	case score < 0.75:
		return policy.RiskHigh
	default:
		return policy.RiskCritical
	}
}

// Assess scores the signals and names everything that raised the score.
//
// A signal that contributed nothing is left out rather than reported as a clean
// record. The factors are therefore exactly the reasons the plan is not
// harmless, and an empty list means there was nothing to say — which is a
// different claim from "we did not look", and the one that matters to whoever
// is being asked to approve.
func Assess(s Signals) policy.RiskAssessment {
	a := policy.RiskAssessment{Level: policy.RiskLow}

	add := func(code string, weight, raw float64, message string) {
		if raw <= 0 {
			return
		}
		a.Score += weight * clamp01(raw)
		a.Factors = append(a.Factors, policy.RiskFactor{Code: code, Message: message})
	}

	envRaw, envWhy := environmentRisk(s.Environment)
	add(FactorEnvironment, weightEnvironment, envRaw, envWhy)

	if n := irreversible(s.Operations); n > 0 {
		add(FactorIrreversible, weightIrreversible, 1, fmt.Sprintf(
			"%d of %d operations cannot be undone", n, len(s.Operations)))
	}
	if n := sensitive(s.Operations); n > 0 {
		add(FactorSensitive, weightSensitive, 1, fmt.Sprintf(
			"%d values that must not be shown would change", n))
	}
	// One operation is not a blast radius. Counting from the first would put
	// the factor on every plan that exists, and a factor every plan carries
	// tells a reviewer nothing.
	if n := len(s.Operations); n > 1 {
		add(FactorBlastRadius, weightBlastRadius, float64(n-1)/float64(wideChange-1), fmt.Sprintf(
			"%d operations, counted as widest at %d", n, wideChange))
	}
	stratRaw, stratWhy := strategyRisk(s.Strategy)
	add(FactorStrategy, weightStrategy, stratRaw, stratWhy)

	a.Score = clamp01(a.Score)
	a.Level = level(a.Score)
	return a
}

func irreversible(ops []plan.PlannedOperation) int {
	n := 0
	for _, o := range ops {
		if !o.Reversible {
			n++
		}
	}
	return n
}

func sensitive(ops []plan.PlannedOperation) int {
	n := 0
	for _, o := range ops {
		for _, c := range o.Diff.Changes {
			if c.Sensitive {
				n++
			}
		}
	}
	return n
}
