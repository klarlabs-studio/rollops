package security

const (
	RoleAdmin       = "admin"
	RoleAgent       = "agent"
	RoleAgentDeploy = "agent-deploy"
)

// DefaultRBACPolicy returns the daemon bootstrap policy.
//
// The bootstrap admin identity is intentionally narrow and explicit:
// "human:admin" is bound only when the daemon receives ROLLOPS_ADMIN_TOKEN.
// Agents are read/plan-only by default; deploy and rollback grants should be
// added deliberately by the operator once the deployment's target scopes are
// known. RoleAgentDeploy exists unbound so a policy file can opt one agent in
// without widening agent:*.
func DefaultRBACPolicy() *Policy {
	policy := NewPolicy()
	policy.DefineRole(Role{Name: RoleAdmin, Grants: []Grant{
		{Perm: PermPlan},
		{Perm: PermApply},
		{Perm: PermApprove},
		{Perm: PermPromote},
		{Perm: PermRollback},
		{Perm: PermStatus},
		{Perm: PermSchedule},
		{Perm: PermFreeze},
	}})
	policy.Bind("human:admin", RoleAdmin)

	policy.DefineRole(Role{Name: RoleAgent, Grants: []Grant{
		{Perm: PermPlan},
		{Perm: PermStatus},
	}})
	policy.Bind("agent:*", RoleAgent)

	policy.DefineRole(Role{Name: RoleAgentDeploy, Grants: []Grant{
		{Perm: PermPlan},
		{Perm: PermApply},
		{Perm: PermRollback},
		{Perm: PermStatus},
		{Perm: PermPromote}, // rollouts.verify is authorized as promote
	}})

	return policy
}
