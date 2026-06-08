package security

import (
	"errors"
	"testing"

	"go.klarlabs.de/rolloffs/internal/rollout"
)

func newPolicy() *Policy {
	p := NewPolicy()
	p.DefineRole(Role{Name: "viewer", Grants: []Grant{{Perm: PermStatus}}})
	p.DefineRole(Role{Name: "operator", Grants: []Grant{
		{Perm: PermStatus}, {Perm: PermPlan}, {Perm: PermApply}, {Perm: PermApprove}, {Perm: PermRollback},
	}})
	// Agents may apply, but only to non-prod (prod stays human-gated here).
	p.DefineRole(Role{Name: "agent-deployer", Grants: []Grant{
		{Perm: PermPlan}, {Perm: PermApply, Scope: Scope{Env: "staging"}}, {Perm: PermApply, Scope: Scope{Env: "dev"}},
	}})
	p.Bind("human:felix", "operator")
	p.Bind("agent:*", "agent-deployer")
	p.Bind("ci:*", "viewer")
	return p
}

var (
	felix = rollout.Identity{Kind: "human", Name: "felix"}
	nomi  = rollout.Identity{Kind: "agent", Name: "nomi"}
	ci    = rollout.Identity{Kind: "ci", Name: "gh-actions"}
)

func TestAuthorize_OperatorCanApplyProd(t *testing.T) {
	p := newPolicy()
	if err := p.Authorize(felix, PermApply, Scope{Env: "prod", TargetRef: "x"}); err != nil {
		t.Errorf("operator should apply to prod: %v", err)
	}
}

func TestAuthorize_AgentBlockedFromProd(t *testing.T) {
	p := newPolicy()
	if p.Can(nomi, PermApply, Scope{Env: "staging"}) != true {
		t.Error("agent should apply to staging")
	}
	err := p.Authorize(nomi, PermApply, Scope{Env: "prod"})
	if err == nil {
		t.Fatal("agent must be blocked from prod apply")
	}
	var forbidden ErrForbidden
	if !errors.As(err, &forbidden) {
		t.Errorf("want ErrForbidden, got %T", err)
	}
}

func TestAuthorize_ViewerReadOnly(t *testing.T) {
	p := newPolicy()
	if !p.Can(ci, PermStatus, Scope{}) {
		t.Error("ci viewer should read status")
	}
	if p.Can(ci, PermApply, Scope{Env: "dev"}) {
		t.Error("ci viewer must not apply")
	}
}

func TestAuthorize_UnknownIdentityDenied(t *testing.T) {
	p := newPolicy()
	stranger := rollout.Identity{Kind: "human", Name: "mallory"}
	if p.Can(stranger, PermStatus, Scope{}) {
		t.Error("unbound identity must be denied")
	}
}
