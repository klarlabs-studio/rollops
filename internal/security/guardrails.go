package security

import (
	"context"
	"errors"
	"sync"
	"time"

	"go.klarlabs.de/fortify/ratelimit"

	"go.klarlabs.de/rolloffs/internal/rollout"
)

// Guardrails are the hard limits beneath the risk gate. Unlike the gate's
// judgement, these are not advisory and an agent cannot lower them: a
// non-bypassable policy floor, an emergency freeze, and a rate limit on
// agent-initiated rollouts.
type Guardrails struct {
	Floor      PolicyFloor
	Freeze     *Freeze
	AgentLimit *AgentLimiter
}

// Sentinel errors.
var (
	ErrFrozen      = errors.New("security: rollouts are frozen (emergency kill-switch active)")
	ErrRateLimited = errors.New("security: agent rate limit exceeded")
)

// CheckApply enforces the guardrails before an apply. It returns ErrFrozen or
// ErrRateLimited to block outright, and forceApproval=true when the policy floor
// requires a human regardless of the computed risk score.
func (g *Guardrails) CheckApply(ctx context.Context, id rollout.Identity, t FloorInput) (forceApproval bool, err error) {
	if g.Freeze != nil {
		if active, _ := g.Freeze.Active(); active {
			return false, ErrFrozen
		}
	}
	if id.Kind == "agent" && g.AgentLimit != nil {
		if !g.AgentLimit.Allow(ctx, id.Name) {
			return false, ErrRateLimited
		}
	}
	return g.Floor.MustApprove(t), nil
}

// FloorInput carries what the policy floor inspects.
type FloorInput struct {
	TargetRef   string
	Environment string
	ChangeType  string
	Criticality string
}

// PolicyFloor lists the conditions that ALWAYS require human approval,
// regardless of computed risk. An agent cannot configure its way past these.
type PolicyFloor struct {
	CriticalTargets           map[string]struct{} // targets always gated
	RequireApprovalProdSchema bool                // prod schema/DB migrations always gated
}

// DefaultPolicyFloor gates prod schema changes and any critical-criticality target.
func DefaultPolicyFloor() PolicyFloor {
	return PolicyFloor{CriticalTargets: map[string]struct{}{}, RequireApprovalProdSchema: true}
}

// MustApprove reports whether the floor forces human approval.
func (f PolicyFloor) MustApprove(in FloorInput) bool {
	if in.Criticality == "critical" {
		return true
	}
	if _, ok := f.CriticalTargets[in.TargetRef]; ok {
		return true
	}
	if f.RequireApprovalProdSchema && in.Environment == "prod" && (in.ChangeType == "schema" || in.ChangeType == "migration") {
		return true
	}
	return false
}

// Freeze is the emergency kill-switch. While active, applies are blocked.
type Freeze struct {
	mu     sync.RWMutex
	active bool
	reason string
	by     rollout.Identity
}

// NewFreeze returns an inactive freeze.
func NewFreeze() *Freeze { return &Freeze{} }

// Engage halts all rollouts.
func (f *Freeze) Engage(by rollout.Identity, reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.active, f.by, f.reason = true, by, reason
}

// Lift releases the freeze.
func (f *Freeze) Lift(by rollout.Identity) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.active, f.by, f.reason = false, by, ""
}

// Active reports the freeze state and reason.
func (f *Freeze) Active() (bool, string) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.active, f.reason
}

// AgentLimiter bounds the blast radius of a misbehaving agent via fortify.
type AgentLimiter struct {
	rl ratelimit.RateLimiter
}

// NewAgentLimiter allows `rate` agent rollouts per `interval`, bursting to rate.
func NewAgentLimiter(rate int, interval time.Duration) *AgentLimiter {
	return &AgentLimiter{rl: ratelimit.New(ratelimit.Config{
		Rate:     rate,
		Burst:    rate,
		Interval: interval,
		Store:    ratelimit.NewMemoryStore(),
	})}
}

// Allow reports whether an agent may proceed, keyed per agent.
func (a *AgentLimiter) Allow(ctx context.Context, agentName string) bool {
	return a.rl.Allow(ctx, "agent:"+agentName)
}
