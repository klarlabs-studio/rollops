// Package security is the trust boundary: RBAC over rollout operations bound to
// human, CI, and agent identities, plus the agent guardrails (non-bypassable
// policy floor, emergency freeze). Every interface — gRPC, REST, MCP — is
// authenticated and authorized through this package; an agent endpoint that can
// deploy is treated as privileged.
package security

import (
	"fmt"
	"maps"
	"sync"

	"go.klarlabs.de/rollops/internal/rollout"
)

// Permission is a grantable operation. apply-to-prod is a distinct, scoped
// permission from status — the scope carries the distinction.
type Permission string

const (
	PermStatus   Permission = "rollouts.status"
	PermPlan     Permission = "rollouts.plan"
	PermApply    Permission = "rollouts.apply"
	PermApprove  Permission = "rollouts.approve" // gates approve + reject (approval authority)
	PermPromote  Permission = "rollouts.promote"
	PermRollback Permission = "rollouts.rollback"
	PermSchedule Permission = "rollouts.schedule"
	PermFreeze   Permission = "rollouts.freeze"
)

// Scope constrains a grant. An empty field is a wildcard.
type Scope struct {
	Env       string // dev | staging | prod
	TargetRef string
}

// matches reports whether this (grant) scope covers the requested scope.
func (g Scope) matches(req Scope) bool {
	if g.Env != "" && g.Env != req.Env {
		return false
	}
	if g.TargetRef != "" && g.TargetRef != req.TargetRef {
		return false
	}
	return true
}

// Grant is a permission within a scope.
type Grant struct {
	Perm  Permission
	Scope Scope
}

// Role is a named set of grants.
type Role struct {
	Name   string
	Grants []Grant
}

// ErrForbidden is returned when authorization fails.
type ErrForbidden struct {
	Identity rollout.Identity
	Perm     Permission
	Scope    Scope
}

func (e ErrForbidden) Error() string {
	return fmt.Sprintf("security: forbidden: %s/%s may not %s on {env=%q target=%q}",
		e.Identity.Kind, e.Identity.Name, e.Perm, e.Scope.Env, e.Scope.TargetRef)
}

// Policy holds roles and binds identities to them. Bindings key on "kind:name"
// with a "kind:*" fallback so all agents (or all CI) can share a role. It is safe
// for concurrent use: Authorize takes a read lock, mutations a write lock, so the
// live policy can be hot-reloaded (ReplaceWith) while requests are in flight.
type Policy struct {
	mu       sync.RWMutex
	roles    map[string]Role
	bindings map[string][]string
}

// NewPolicy returns an empty policy.
func NewPolicy() *Policy {
	return &Policy{roles: map[string]Role{}, bindings: map[string][]string{}}
}

// DefineRole registers a role.
func (p *Policy) DefineRole(r Role) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.roles[r.Name] = r
}

// hasRole reports whether a role is defined.
func (p *Policy) hasRole(name string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	_, ok := p.roles[name]
	return ok
}

// Bind grants roles to an identity key ("agent:nomi", "human:felix", "ci:*").
func (p *Policy) Bind(identityKey string, roles ...string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.bindings[identityKey] = append(p.bindings[identityKey], roles...)
}

// ReplaceWith atomically swaps this policy's roles and bindings for another's,
// so an operator can hot-reload the policy file (e.g. on SIGHUP) without dropping
// in-flight requests or restarting the daemon.
func (p *Policy) ReplaceWith(other *Policy) {
	other.mu.RLock()
	roles := make(map[string]Role, len(other.roles))
	maps.Copy(roles, other.roles)
	bindings := make(map[string][]string, len(other.bindings))
	for k, v := range other.bindings {
		bindings[k] = append([]string(nil), v...)
	}
	other.mu.RUnlock()

	p.mu.Lock()
	p.roles, p.bindings = roles, bindings
	p.mu.Unlock()
}

// rolesFor assumes the read lock is held.
func (p *Policy) rolesFor(id rollout.Identity) []string {
	var out []string
	out = append(out, p.bindings[id.Kind+":"+id.Name]...)
	out = append(out, p.bindings[id.Kind+":*"]...)
	for _, g := range id.Groups {
		out = append(out, p.bindings["group:"+g]...)
	}
	if len(id.Groups) > 0 {
		out = append(out, p.bindings["group:*"]...)
	}
	return out
}

// Authorize returns nil if id may perform perm within scope, else ErrForbidden.
func (p *Policy) Authorize(id rollout.Identity, perm Permission, scope Scope) error {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, roleName := range p.rolesFor(id) {
		role, ok := p.roles[roleName]
		if !ok {
			continue
		}
		for _, g := range role.Grants {
			if g.Perm == perm && g.Scope.matches(scope) {
				return nil
			}
		}
	}
	return ErrForbidden{Identity: id, Perm: perm, Scope: scope}
}

// Can is the boolean form of Authorize.
func (p *Policy) Can(id rollout.Identity, perm Permission, scope Scope) bool {
	return p.Authorize(id, perm, scope) == nil
}
