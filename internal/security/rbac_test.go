package security

import (
	"errors"
	"testing"

	"go.klarlabs.de/rollops/internal/rollout"
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

func TestAuthorize_ExternalGroupBinding(t *testing.T) {
	p := NewPolicy()
	p.DefineRole(Role{Name: "operator", Grants: []Grant{{Perm: PermApply}}})
	p.Bind("group:rollops-operators", "operator")
	id := rollout.Identity{Kind: "human", Name: "oidc-user", Groups: []string{"rollops-operators"}}
	if err := p.Authorize(id, PermApply, Scope{TargetRef: "svc/prod/api"}); err != nil {
		t.Fatalf("group-bound identity should authorize: %v", err)
	}
}

func TestDefaultRBACPolicy_AdminHasBootstrapOperations(t *testing.T) {
	p := DefaultRBACPolicy()
	admin := rollout.Identity{Kind: "human", Name: "admin"}
	for _, perm := range []Permission{PermPlan, PermApply, PermApprove, PermRollback, PermStatus, PermSchedule, PermFreeze} {
		if !p.Can(admin, perm, Scope{TargetRef: "svc/prod/api"}) {
			t.Errorf("admin should have %s", perm)
		}
	}
}

func TestDefaultRBACPolicy_AgentIsPlanAndStatusOnly(t *testing.T) {
	p := DefaultRBACPolicy()
	agent := rollout.Identity{Kind: "agent", Name: "nomi"}
	if !p.Can(agent, PermPlan, Scope{TargetRef: "svc/prod/api"}) {
		t.Error("agent should plan by default")
	}
	if !p.Can(agent, PermStatus, Scope{}) {
		t.Error("agent should read status by default")
	}
	for _, perm := range []Permission{PermApply, PermRollback, PermApprove, PermPromote, PermSchedule, PermFreeze} {
		if p.Can(agent, perm, Scope{TargetRef: "svc/prod/api"}) {
			t.Errorf("agent should not have %s by default", perm)
		}
	}
}

func TestDefaultRBACPolicy_AgentDeployIsOptIn(t *testing.T) {
	p := DefaultRBACPolicy()
	agent := rollout.Identity{Kind: "agent", Name: "nomi"}
	if p.Can(agent, PermApply, Scope{}) {
		t.Fatal("unbound agent-deploy must not widen agent:*")
	}
	p.Bind("agent:nomi", RoleAgentDeploy)
	for _, perm := range []Permission{PermPlan, PermApply, PermRollback, PermStatus, PermPromote} {
		if !p.Can(agent, perm, Scope{TargetRef: "svc/prod/api"}) {
			t.Errorf("agent-deploy should grant %s", perm)
		}
	}
	for _, perm := range []Permission{PermApprove, PermFreeze, PermSchedule} {
		if p.Can(agent, perm, Scope{}) {
			t.Errorf("agent-deploy must not grant %s", perm)
		}
	}
}

func TestApplyPolicyFile_BindsBuiltInAgentDeploy(t *testing.T) {
	p := DefaultRBACPolicy()
	doc := []byte("bindings:\n  - subject: agent:nomi\n    roles: [agent-deploy]\n")
	if err := ApplyPolicyFile(p, doc); err != nil {
		t.Fatal(err)
	}
	agent := rollout.Identity{Kind: "agent", Name: "nomi"}
	if !p.Can(agent, PermApply, Scope{}) {
		t.Error("policy file should bind the built-in agent-deploy role")
	}
	if p.Can(agent, PermApprove, Scope{}) {
		t.Error("agent-deploy must still not approve")
	}
}
