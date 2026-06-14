// Package mcp is the agent surface: an MCP server (mcp-go) exposing the engine
// operations 1:1 as tools — rollouts.plan, rollouts.apply, rollouts.status,
// rollouts.rollback. It is embedded in the daemon by default but runnable
// standalone. The agent endpoint can deploy, so it is privileged: every tool
// authorizes through the same RBAC policy as the other interfaces, against the
// identity the MCP connection authenticated as.
package mcp

import (
	"context"

	mcpserver "go.klarlabs.de/mcp"

	"go.klarlabs.de/rollops/internal/config"
	"go.klarlabs.de/rollops/internal/engine"
	"go.klarlabs.de/rollops/internal/rollout"
	"go.klarlabs.de/rollops/internal/security"
)

// Tools holds the engine surface plus the authenticated agent identity and the
// RBAC policy. Tool handlers are methods on it.
type Tools struct {
	eng      *engine.Engine
	policy   *security.Policy
	identity rollout.Identity
}

// NewTools binds the engine, policy, and the identity the MCP connection runs as.
func NewTools(eng *engine.Engine, policy *security.Policy, id rollout.Identity) *Tools {
	return &Tools{eng: eng, policy: policy, identity: id}
}

// --- tool I/O ---

// PlanInput is the input to rollouts.plan.
type PlanInput struct {
	Config string `json:"config" jsonschema:"the rollout config YAML"`
}

// PlanOutput is the result of rollouts.plan.
type PlanOutput struct {
	Action  string `json:"action"`
	Changed bool   `json:"changed"`
	Summary string `json:"summary"`
}

// ApplyInput is the input to rollouts.apply.
type ApplyInput struct {
	Config string `json:"config" jsonschema:"the rollout config YAML"`
}

// ApplyOutput is the result of rollouts.apply.
type ApplyOutput struct {
	RolloutID string `json:"rollout_id"`
	Phase     string `json:"phase"`
	Target    string `json:"target"`
}

// StatusInput is the input to rollouts.status.
type StatusInput struct {
	RolloutID string `json:"rollout_id" jsonschema:"the rollout id to look up"`
}

// StatusOutput is the result of rollouts.status.
type StatusOutput struct {
	RolloutID string `json:"rollout_id"`
	Phase     string `json:"phase"`
	Target    string `json:"target"`
	Note      string `json:"note,omitempty"`
}

// RollbackInput is the input to rollouts.rollback.
type RollbackInput struct {
	TargetRef string `json:"target_ref" jsonschema:"the target to roll back to its previous desired state"`
	Force     bool   `json:"force,omitempty" jsonschema:"override the backward-compatibility gate for a non-backwardCompatible migration with no reverse command"`
}

// RollbackOutput is the result of rollouts.rollback.
type RollbackOutput struct {
	RolloutID string `json:"rollout_id"`
	Phase     string `json:"phase"`
	Target    string `json:"target"`
}

// --- handlers ---

// Plan implements rollouts.plan.
func (t *Tools) Plan(ctx context.Context, in PlanInput) (PlanOutput, error) {
	c, err := config.Load([]byte(in.Config))
	if err != nil {
		return PlanOutput{}, err
	}
	if err := t.authz(security.PermPlan, c); err != nil {
		return PlanOutput{}, err
	}
	p, err := t.eng.Plan(ctx, c)
	if err != nil {
		return PlanOutput{}, err
	}
	return PlanOutput{Action: string(p.Action), Changed: p.Changed, Summary: p.Summary}, nil
}

// Apply implements rollouts.apply. The agent must have planned first; the engine
// produces a plan here so the plan-before-apply guard holds.
func (t *Tools) Apply(ctx context.Context, in ApplyInput) (ApplyOutput, error) {
	c, err := config.Load([]byte(in.Config))
	if err != nil {
		return ApplyOutput{}, err
	}
	if err := t.authz(security.PermApply, c); err != nil {
		return ApplyOutput{}, err
	}
	if _, err := t.eng.Plan(ctx, c); err != nil {
		return ApplyOutput{}, err
	}
	r, err := t.eng.Apply(ctx, engine.ApplyRequest{Config: c, Initiator: t.identity, Planned: true})
	if err != nil {
		return ApplyOutput{}, err
	}
	return ApplyOutput{RolloutID: r.ID, Phase: string(r.Phase), Target: r.TargetRef}, nil
}

// Status implements rollouts.status.
func (t *Tools) Status(ctx context.Context, in StatusInput) (StatusOutput, error) {
	if err := t.policy.Authorize(t.identity, security.PermStatus, security.Scope{}); err != nil {
		return StatusOutput{}, err
	}
	r, err := t.eng.Status(ctx, in.RolloutID)
	if err != nil {
		return StatusOutput{}, err
	}
	return StatusOutput{RolloutID: r.ID, Phase: string(r.Phase), Target: r.TargetRef, Note: r.Note}, nil
}

// Rollback implements rollouts.rollback.
func (t *Tools) Rollback(ctx context.Context, in RollbackInput) (RollbackOutput, error) {
	if err := t.policy.Authorize(t.identity, security.PermRollback, security.Scope{TargetRef: in.TargetRef}); err != nil {
		return RollbackOutput{}, err
	}
	r, err := t.eng.RollbackLast(ctx, in.TargetRef, in.Force)
	if err != nil {
		return RollbackOutput{}, err
	}
	return RollbackOutput{RolloutID: r.ID, Phase: string(r.Phase), Target: r.TargetRef}, nil
}

func (t *Tools) authz(perm security.Permission, c *config.Config) error {
	return t.policy.Authorize(t.identity, perm, security.Scope{Env: c.Spec.Target.Env, TargetRef: c.Spec.Target.Ref})
}

// ActionInput identifies a rollout by id for approve/reject/promote.
type ActionInput struct {
	RolloutID string `json:"rollout_id" jsonschema:"the rollout id to act on"`
}

// ActionOutput is the result of approve/reject/promote.
type ActionOutput struct {
	RolloutID string `json:"rollout_id"`
	Phase     string `json:"phase"`
	Target    string `json:"target"`
	Note      string `json:"note,omitempty"`
}

// Approve implements rollouts.approve.
func (t *Tools) Approve(ctx context.Context, in ActionInput) (ActionOutput, error) {
	return t.action(ctx, in.RolloutID, security.PermApprove, func(id string) (rollout.Rollout, error) {
		return t.eng.Approve(ctx, id, t.identity)
	})
}

// Reject implements rollouts.reject.
func (t *Tools) Reject(ctx context.Context, in ActionInput) (ActionOutput, error) {
	return t.action(ctx, in.RolloutID, security.PermApprove, func(id string) (rollout.Rollout, error) {
		return t.eng.Reject(ctx, id, t.identity)
	})
}

// Promote implements rollouts.promote.
func (t *Tools) Promote(ctx context.Context, in ActionInput) (ActionOutput, error) {
	return t.action(ctx, in.RolloutID, security.PermPromote, func(id string) (rollout.Rollout, error) {
		return t.eng.Promote(ctx, id)
	})
}

// FreezeInput toggles the emergency kill-switch.
type FreezeInput struct {
	Active bool   `json:"active" jsonschema:"true engages the freeze (blocks all applies), false lifts it"`
	Reason string `json:"reason,omitempty" jsonschema:"recorded when engaging"`
}

// FreezeOutput is the resulting freeze state.
type FreezeOutput struct {
	Active bool   `json:"active"`
	Reason string `json:"reason,omitempty"`
}

// Freeze implements rollouts.freeze.
func (t *Tools) Freeze(ctx context.Context, in FreezeInput) (FreezeOutput, error) {
	if err := t.policy.Authorize(t.identity, security.PermFreeze, security.Scope{}); err != nil {
		return FreezeOutput{}, err
	}
	active, reason, err := t.eng.Freeze(ctx, in.Active, t.identity, in.Reason)
	if err != nil {
		return FreezeOutput{}, err
	}
	return FreezeOutput{Active: active, Reason: reason}, nil
}

// action is the shared approve/reject/promote flow: scope authorization to the
// rollout's target, run the engine op, return its outcome.
func (t *Tools) action(ctx context.Context, id string, perm security.Permission, op func(string) (rollout.Rollout, error)) (ActionOutput, error) {
	cur, err := t.eng.Status(ctx, id)
	if err != nil {
		return ActionOutput{}, err
	}
	if err := t.policy.Authorize(t.identity, perm, security.Scope{TargetRef: cur.TargetRef}); err != nil {
		return ActionOutput{}, err
	}
	r, err := op(id)
	if err != nil {
		return ActionOutput{}, err
	}
	return ActionOutput{RolloutID: r.ID, Phase: string(r.Phase), Target: r.TargetRef, Note: r.Note}, nil
}

// NewServer builds an MCP server with the rollout tools registered. Serve it via
// mcp.ServeStdio / ServeHTTP (embedded in the daemon or standalone).
func NewServer(t *Tools) *mcpserver.Server {
	srv := mcpserver.NewServer(mcpserver.ServerInfo{
		Name:         "rollops",
		Version:      "0.1.0",
		Capabilities: mcpserver.Capabilities{Tools: true},
	})
	Register(srv, t)
	return srv
}

// Register wires the rollout tools onto an existing server (1:1 with engine ops).
func Register(srv *mcpserver.Server, t *Tools) {
	srv.Tool("rollouts.plan").Description("Show what an apply would change for a rollout config").Handler(t.Plan)
	srv.Tool("rollouts.apply").Description("Deploy desired state from a rollout config").Handler(t.Apply)
	srv.Tool("rollouts.rollback").Description("Roll back a target to its previous desired state").Handler(t.Rollback)
	srv.Tool("rollouts.approve").Description("Approve a rollout awaiting approval (deploys it)").Handler(t.Approve)
	srv.Tool("rollouts.reject").Description("Reject a rollout awaiting approval").Handler(t.Reject)
	srv.Tool("rollouts.promote").Description("Promote a verified rollout to complete").Handler(t.Promote)
	srv.Tool("rollouts.freeze").Description("Engage or lift the emergency freeze that blocks all applies").Handler(t.Freeze)
	srv.Tool("rollouts.status").Description("Get the current state of a rollout by id").Handler(t.Status)
}
