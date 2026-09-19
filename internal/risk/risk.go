// Package risk is the blast-radius scorer. It turns observability-free signals
// — target criticality, environment, change type, blast radius, rollout
// strategy, and optional recent rollback history — into a normalized [0,1]
// score with the factors that produced it.
//
// What it does NOT do is authorize. The gate here hands back a
// policyv1.PolicyDecision carrying the assessment as evidence, so the score is
// an input to the authorization decision rather than the decision itself
// (§12.4). A scorer that could refuse a deploy on its own would be the sole
// authorization mechanism the spec forbids, and nothing else would get a say.
//
// Note: decisionkit's risk engine scores commitment/deadline risk, a different
// shape; the blast-radius model here is dedicated to rollouts. The gate's
// threshold and the "sensitive" override are CEL (internal/condition), so the
// operator configures behaviour without code.
package risk

import (
	"fmt"

	"go.klarlabs.de/rollops/internal/condition"
	policyv1 "go.klarlabs.de/rollops/pkg/policy/v1"
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

// Factor names, stable so a surface can pick one out of an assessment without
// matching on prose.
const (
	FactorCriticality = "criticality"
	FactorEnvironment = "environment"
	FactorChangeType  = "changeType"
	FactorBlastRadius = "blastRadius"
	FactorStrategy    = "strategy"
	FactorHistory     = "history"
)

// Reason codes the gate attaches to its decision, so a caller can tell which
// rule produced the requirement without reading the message.
const (
	ReasonSensitive      = "sensitive"
	ReasonThreshold      = "risk_threshold"
	ReasonBelowThreshold = "below_threshold"
)

// levelOf bands a score for humans and for CEL. The bands are quartiles: the
// number is the authority, the band is how it gets talked about.
func levelOf(score float64) policyv1.RiskLevel {
	switch {
	case score < 0.25:
		return policyv1.RiskLow
	case score < 0.5:
		return policyv1.RiskMedium
	case score < 0.75:
		return policyv1.RiskHigh
	default:
		return policyv1.RiskCritical
	}
}

// Assess scores the signals and explains the score factor by factor, which is
// §12.4's "SHOULD include explainable factors". A number nobody can decompose
// is a number nobody can argue with, and the operator deciding whether to
// approve is exactly the person who has to argue with it.
//
// Each factor's Weight is its share of the final score, so the factors sum to
// it. A signal whose weight is zero — history, until risk.history is configured
// — is left out rather than reported as a clean record: "we looked and found
// nothing" and "we never looked" are different claims.
func Assess(s Signals, w Weights) policyv1.RiskAssessment {
	if (w == Weights{}) {
		w = DefaultWeights()
	}
	maxBlast := w.MaxBlastRadius
	if maxBlast <= 0 {
		maxBlast = 10
	}
	total := w.Criticality + w.Environment + w.ChangeType + w.BlastRadius + w.Strategy + w.History
	a := policyv1.RiskAssessment{Score: Score(s, w)}
	a.Level = levelOf(a.Score)
	if total == 0 {
		return a
	}
	add := func(name string, weight, raw float64, detail string) {
		if weight == 0 {
			return
		}
		a.Factors = append(a.Factors, policyv1.RiskFactor{
			Name:   name,
			Weight: weight * raw / total,
			Detail: detail,
		})
	}
	add(FactorCriticality, w.Criticality, criticalityScore(s.Criticality), orUnset(s.Criticality))
	add(FactorEnvironment, w.Environment, environmentScore(s.Environment), orUnset(s.Environment))
	add(FactorChangeType, w.ChangeType, changeTypeScore(s.ChangeType), orUnset(s.ChangeType))
	add(FactorBlastRadius, w.BlastRadius, clamp01(float64(s.BlastRadius)/float64(maxBlast)),
		fmt.Sprintf("%d dependent(s), saturating at %d", s.BlastRadius, maxBlast))
	add(FactorStrategy, w.Strategy, strategyScore(s.Strategy), orUnset(s.Strategy))
	add(FactorHistory, w.History, HistoryScore(s.RecentFailures, w.MaxRecentFailures),
		fmt.Sprintf("%d recent rollback(s)", s.RecentFailures))
	return a
}

func orUnset(v string) string {
	if v == "" {
		return "unset"
	}
	return v
}

// Gate turns an assessment into an authorization decision, using a threshold
// and an optional CEL "sensitive" expression. Below threshold and not sensitive
// → the operation proceeds; otherwise it proceeds once a human approves.
type Gate struct {
	Threshold     float64
	Weights       Weights
	SensitiveExpr string // CEL bool over the rollout decision vars; empty disables
}

// Evaluate scores the signals and decides.
//
// An above-threshold change is an ALLOWED operation with a condition attached,
// not a denial. That is the shape §12.4 asks for and it is load-bearing
// downstream: the requirement is what sends a rollout to awaiting-approval,
// where a denial would send it to failed.
func (g Gate) Evaluate(s Signals) (policyv1.PolicyDecision, error) {
	w := g.Weights
	if (w == Weights{}) {
		w = DefaultWeights()
	}
	a := Assess(s, w)
	d := policyv1.PolicyDecision{Allowed: true, Risk: a}

	sensitive := false
	if g.SensitiveExpr != "" {
		var err error
		sensitive, err = condition.Eval(g.SensitiveExpr, condition.Input{
			Criticality:    s.Criticality,
			Environment:    s.Environment,
			ChangeType:     s.ChangeType,
			BlastRadius:    s.BlastRadius,
			Strategy:       s.Strategy,
			Score:          a.Score,
			RecentFailures: s.RecentFailures,
			HistoryRisk:    HistoryScore(s.RecentFailures, w.MaxRecentFailures),
		})
		if err != nil {
			// Fail CLOSED: an expression that would not compile decided nothing,
			// and the zero decision denies. A caller that drops the error must
			// not find permission in what it was handed.
			return policyv1.PolicyDecision{}, fmt.Errorf("risk: sensitive expression: %w", err)
		}
	}

	var code, msg string
	switch {
	case sensitive:
		code, msg = ReasonSensitive, fmt.Sprintf("sensitive: flagged by policy (score %.2f)", a.Score)
	case a.Score >= g.Threshold:
		code, msg = ReasonThreshold, fmt.Sprintf("score %.2f >= threshold %.2f", a.Score, g.Threshold)
	default:
		code, msg = ReasonBelowThreshold, fmt.Sprintf("score %.2f < threshold %.2f: auto-proceed", a.Score, g.Threshold)
	}
	d.Reasons = []policyv1.Reason{{Code: code, Message: msg}}
	if code != ReasonBelowThreshold {
		d.Requirements = []policyv1.Requirement{{Kind: policyv1.RequireHumanApproval, Count: 1, Detail: msg}}
	}
	return d, nil
}

// Sensitive reports whether the gate flagged the change through its CEL
// expression rather than through the threshold. The two mean different things
// to an operator — one is a rule about this kind of change, the other is a
// number — so the surfaces keep them apart.
func Sensitive(d policyv1.PolicyDecision) bool {
	for _, r := range d.Reasons {
		if r.Code == ReasonSensitive {
			return true
		}
	}
	return false
}

// Explain renders the gate's single citeable reason.
func Explain(d policyv1.PolicyDecision) string {
	if len(d.Reasons) == 0 {
		return ""
	}
	return d.Reasons[0].Message
}
