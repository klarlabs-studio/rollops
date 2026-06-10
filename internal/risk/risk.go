// Package risk is the blast-radius risk gate. It scores a proposed rollout from
// observability-free signals — target criticality, environment, change type,
// blast radius, rollout strategy, and optional recent rollback history — into a
// normalized [0,1] score, and decides whether the change may auto-proceed or
// requires a single human approval.
//
// Note: decisionkit's risk engine scores commitment/deadline risk, a different
// shape; the blast-radius model here is dedicated to rollouts. The gate's
// threshold and the "sensitive" override are CEL (internal/condition), so the
// operator configures behaviour without code.
package risk

import (
	"fmt"

	"go.klarlabs.de/rollops/internal/condition"
)

// Signals are the observability-free inputs to the score.
type Signals struct {
	Criticality    string // low | medium | high | critical
	Environment    string // dev | staging | prod
	ChangeType     string // config | code | schema
	BlastRadius    int    // count of downstream dependents
	Strategy       string // rolling | canary | blue-green
	RecentFailures int    // rolled-back records inside the configured lookback
}

// Weights tunes each signal's contribution; they need not sum to 1 (the score
// is normalized by their total).
type Weights struct {
	Criticality float64
	Environment float64
	ChangeType  float64
	BlastRadius float64
	Strategy    float64
	History     float64
	// MaxBlastRadius is the dependent count treated as maximum risk (saturates).
	MaxBlastRadius int
	// MaxRecentFailures is the rollback count treated as maximum history risk.
	MaxRecentFailures int
}

// DefaultWeights are sensible safe defaults — criticality and environment lead.
// History is opt-in so existing thresholds keep their semantics until
// risk.history is configured.
func DefaultWeights() Weights {
	return Weights{
		Criticality:       0.25,
		Environment:       0.20,
		ChangeType:        0.20,
		BlastRadius:       0.20,
		Strategy:          0.15,
		MaxBlastRadius:    10,
		MaxRecentFailures: 3,
	}
}

func criticalityScore(s string) float64 {
	switch s {
	case "critical":
		return 1
	case "high":
		return 0.66
	case "medium":
		return 0.33
	default: // low / unknown
		return 0
	}
}

func environmentScore(s string) float64 {
	switch s {
	case "prod", "production":
		return 1
	case "staging":
		return 0.5
	default: // dev / unknown
		return 0
	}
}

func changeTypeScore(s string) float64 {
	switch s {
	case "schema", "migration":
		return 1
	case "code":
		return 0.5
	default: // config / unknown
		return 0
	}
}

// strategyScore: full cutover is riskier than a small canary.
func strategyScore(s string) float64 {
	switch s {
	case "blue-green":
		return 1
	case "rolling":
		return 0.5
	default: // canary / unknown
		return 0
	}
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// Score returns the normalized blast-radius score in [0,1].
func Score(s Signals, w Weights) float64 {
	maxBlast := w.MaxBlastRadius
	if maxBlast <= 0 {
		maxBlast = 10
	}
	blast := clamp01(float64(s.BlastRadius) / float64(maxBlast))
	history := HistoryScore(s.RecentFailures, w.MaxRecentFailures)

	total := w.Criticality + w.Environment + w.ChangeType + w.BlastRadius + w.Strategy + w.History
	if total == 0 {
		return 0
	}
	weighted := w.Criticality*criticalityScore(s.Criticality) +
		w.Environment*environmentScore(s.Environment) +
		w.ChangeType*changeTypeScore(s.ChangeType) +
		w.BlastRadius*blast +
		w.Strategy*strategyScore(s.Strategy) +
		w.History*history
	return clamp01(weighted / total)
}

// HistoryScore normalizes recent rollback count into [0,1].
func HistoryScore(recentFailures, maxRecentFailures int) float64 {
	if maxRecentFailures <= 0 {
		maxRecentFailures = 3
	}
	return clamp01(float64(recentFailures) / float64(maxRecentFailures))
}

// Decision is the gate's verdict.
type Decision struct {
	Score         float64
	Threshold     float64
	NeedsApproval bool
	Sensitive     bool // matched the sensitive CEL expression
	Reason        string
}

// Gate evaluates Signals against a threshold and an optional CEL "sensitive"
// expression. Below threshold and not sensitive → auto-proceed; otherwise a
// single human approval is required.
type Gate struct {
	Threshold     float64
	Weights       Weights
	SensitiveExpr string // CEL bool over the rollout decision vars; empty disables
}

// Evaluate scores the signals and decides.
func (g Gate) Evaluate(s Signals) (Decision, error) {
	w := g.Weights
	if (w == Weights{}) {
		w = DefaultWeights()
	}
	score := Score(s, w)
	d := Decision{Score: score, Threshold: g.Threshold}

	if g.SensitiveExpr != "" {
		sensitive, err := condition.Eval(g.SensitiveExpr, condition.Input{
			Criticality:    s.Criticality,
			Environment:    s.Environment,
			ChangeType:     s.ChangeType,
			BlastRadius:    s.BlastRadius,
			Strategy:       s.Strategy,
			Score:          score,
			RecentFailures: s.RecentFailures,
			HistoryRisk:    HistoryScore(s.RecentFailures, w.MaxRecentFailures),
		})
		if err != nil {
			return Decision{}, fmt.Errorf("risk: sensitive expression: %w", err)
		}
		d.Sensitive = sensitive
	}

	d.NeedsApproval = d.Sensitive || score >= g.Threshold
	switch {
	case d.Sensitive:
		d.Reason = fmt.Sprintf("sensitive: flagged by policy (score %.2f)", score)
	case d.NeedsApproval:
		d.Reason = fmt.Sprintf("score %.2f >= threshold %.2f", score, g.Threshold)
	default:
		d.Reason = fmt.Sprintf("score %.2f < threshold %.2f: auto-proceed", score, g.Threshold)
	}
	return d, nil
}
