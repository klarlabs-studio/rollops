package security

import (
	"os"
	"testing"

	"go.klarlabs.de/rollops/internal/rollout"
)

const samplePolicy = `
roles:
  - name: backend-deployer
    grants:
      - perm: rollouts.apply
        env: staging
      - perm: rollouts.rollback
        targetRef: demo/staging/api
      - perm: rollouts.status
bindings:
  - subject: group:backend-team
    roles: [backend-deployer]
  - subject: human:alice
    roles: [admin]
`

func TestApplyPolicyFile_RolesAndBindings(t *testing.T) {
	p := DefaultRBACPolicy()
	if err := ApplyPolicyFile(p, []byte(samplePolicy)); err != nil {
		t.Fatalf("ApplyPolicyFile: %v", err)
	}
	dev := rollout.Identity{Kind: "human", Name: "bob", Groups: []string{"backend-team"}}

	// Granted: apply to staging.
	if err := p.Authorize(dev, PermApply, Scope{Env: "staging", TargetRef: "demo/staging/api"}); err != nil {
		t.Errorf("backend-team should apply to staging: %v", err)
	}
	// Denied: apply to prod (grant is env-scoped to staging).
	if err := p.Authorize(dev, PermApply, Scope{Env: "prod", TargetRef: "demo/prod/api"}); err == nil {
		t.Error("env-scoped grant must NOT allow prod apply")
	}
	// Denied: rollback a target the grant doesn't cover.
	if err := p.Authorize(dev, PermRollback, Scope{TargetRef: "demo/staging/other"}); err == nil {
		t.Error("targetRef-scoped grant must not cover a different target")
	}
	// Granted: rollback the exact scoped target.
	if err := p.Authorize(dev, PermRollback, Scope{TargetRef: "demo/staging/api"}); err != nil {
		t.Errorf("scoped rollback should be allowed: %v", err)
	}
	// File binding to a default role (admin) works.
	alice := rollout.Identity{Kind: "human", Name: "alice"}
	if err := p.Authorize(alice, PermFreeze, Scope{}); err != nil {
		t.Errorf("alice bound to admin should freeze: %v", err)
	}
}

func TestApplyPolicyFile_Rejects(t *testing.T) {
	cases := map[string]string{
		"unknown perm":            "roles:\n  - name: r\n    grants:\n      - perm: rollouts.nope\n",
		"binding to missing role": "bindings:\n  - subject: group:x\n    roles: [ghost]\n",
		"role without name":       "roles:\n  - grants:\n      - perm: rollouts.status\n",
		"binding without subject": "bindings:\n  - roles: [admin]\n",
		"unknown field":           "roles:\n  - name: r\n    bogus: 1\n",
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			if err := ApplyPolicyFile(DefaultRBACPolicy(), []byte(doc)); err == nil {
				t.Errorf("%s must error", name)
			}
		})
	}
}

func TestApplyPolicyFile_EnvWildcardGrantStillMatchesScopedRequest(t *testing.T) {
	// A grant with no env (wildcard) must still authorize an env-tagged request —
	// preserves behavior for policies that don't use env scoping.
	p := NewPolicy()
	if err := ApplyPolicyFile(p, []byte("roles:\n  - name: r\n    grants:\n      - perm: rollouts.apply\nbindings:\n  - subject: ci:*\n    roles: [r]\n")); err != nil {
		t.Fatal(err)
	}
	id := rollout.Identity{Kind: "ci", Name: "pipeline"}
	if err := p.Authorize(id, PermApply, Scope{Env: "prod", TargetRef: "x"}); err != nil {
		t.Errorf("env-wildcard grant should match an env-tagged request: %v", err)
	}
}

func TestPolicy_ReplaceWith_HotSwap(t *testing.T) {
	live := DefaultRBACPolicy()
	dev := rollout.Identity{Kind: "human", Name: "dev", Groups: []string{"team"}}
	// Not yet granted.
	if err := live.Authorize(dev, PermApply, Scope{}); err == nil {
		t.Fatal("dev should not have apply before reload")
	}
	// Build a fresh policy that grants it, swap in.
	fresh := DefaultRBACPolicy()
	if err := ApplyPolicyFile(fresh, []byte("roles:\n  - name: dep\n    grants:\n      - perm: rollouts.apply\nbindings:\n  - subject: group:team\n    roles: [dep]\n")); err != nil {
		t.Fatal(err)
	}
	live.ReplaceWith(fresh)
	if err := live.Authorize(dev, PermApply, Scope{}); err != nil {
		t.Errorf("dev should have apply after ReplaceWith: %v", err)
	}
}

func TestPolicy_ConcurrentAuthorizeAndReplace(t *testing.T) {
	// Run with -race: Authorize (RLock) must not race ReplaceWith (Lock).
	live := DefaultRBACPolicy()
	id := rollout.Identity{Kind: "human", Name: "admin"}
	done := make(chan struct{})
	go func() {
		for range 1000 {
			_ = live.Authorize(id, PermApply, Scope{})
		}
		close(done)
	}()
	for range 1000 {
		live.ReplaceWith(DefaultRBACPolicy())
	}
	<-done
}

func TestApplyPolicyFile_EmptyIsNoop(t *testing.T) {
	p := DefaultRBACPolicy()
	if err := ApplyPolicyFile(p, []byte("")); err != nil {
		t.Fatalf("empty policy should be a no-op, got %v", err)
	}
	// Defaults still intact.
	if err := p.Authorize(rollout.Identity{Kind: "human", Name: "admin"}, PermApply, Scope{}); err != nil {
		t.Errorf("default admin still works: %v", err)
	}
}

func TestApplyPolicyFile_ShippedAgentDeploySnippet(t *testing.T) {
	data, err := os.ReadFile("../../docs/rbac-agent-deploy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	p := DefaultRBACPolicy()
	if err := ApplyPolicyFile(p, data); err != nil {
		t.Fatal(err)
	}
	nomi := rollout.Identity{Kind: "agent", Name: "nomi"}
	other := rollout.Identity{Kind: "agent", Name: "other"}
	if !p.Can(nomi, PermApply, Scope{}) {
		t.Error("nomi bound to agent-deploy should apply")
	}
	if p.Can(other, PermApply, Scope{}) {
		t.Error("unbound agents must stay plan+status")
	}
}
