package security

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	"gopkg.in/yaml.v3"
)

// policyFile is the on-disk RBAC policy: custom roles and identity→role
// bindings, layered onto the bootstrap DefaultRBACPolicy so operators define
// "group:backend may apply to staging" without recompiling the daemon.
//
//	roles:
//	  - name: backend-deployer
//	    grants:
//	      - perm: rollouts.apply
//	        env: staging
//	      - perm: rollouts.rollback
//	        targetRef: demo/staging/api
//	      - perm: rollouts.status
//	bindings:
//	  - subject: group:backend-team
//	    roles: [backend-deployer]
//	  - subject: human:alice
//	    roles: [admin]
type policyFile struct {
	Roles []struct {
		Name   string `yaml:"name"`
		Grants []struct {
			Perm      string `yaml:"perm"`
			Env       string `yaml:"env"`
			TargetRef string `yaml:"targetRef"`
		} `yaml:"grants"`
	} `yaml:"roles"`
	Bindings []struct {
		Subject string   `yaml:"subject"`
		Roles   []string `yaml:"roles"`
	} `yaml:"bindings"`
}

var knownPerms = map[Permission]bool{
	PermStatus: true, PermPlan: true, PermApply: true, PermApprove: true,
	PermPromote: true, PermRollback: true, PermSchedule: true, PermFreeze: true,
}

// LoadPolicyFile reads an RBAC policy file and applies it to p.
func LoadPolicyFile(p *Policy, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("security: read policy file: %w", err)
	}
	return ApplyPolicyFile(p, data)
}

// ApplyPolicyFile parses YAML policy and layers its roles and bindings onto p.
// Roles defined here override same-named roles; bindings are additive. It
// validates that every grant names a known permission and every binding
// references a role that exists (after this file's roles are applied).
func ApplyPolicyFile(p *Policy, data []byte) error {
	var pf policyFile
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&pf); err != nil {
		if errors.Is(err, io.EOF) {
			return nil // empty file → no-op
		}
		return fmt.Errorf("security: parse policy file: %w", err)
	}
	for _, r := range pf.Roles {
		if r.Name == "" {
			return fmt.Errorf("security: policy role: name is required")
		}
		grants := make([]Grant, 0, len(r.Grants))
		for _, g := range r.Grants {
			perm := Permission(g.Perm)
			if !knownPerms[perm] {
				return fmt.Errorf("security: policy role %q: unknown permission %q", r.Name, g.Perm)
			}
			grants = append(grants, Grant{Perm: perm, Scope: Scope{Env: g.Env, TargetRef: g.TargetRef}})
		}
		p.DefineRole(Role{Name: r.Name, Grants: grants})
	}
	for _, b := range pf.Bindings {
		if b.Subject == "" {
			return fmt.Errorf("security: policy binding: subject is required")
		}
		for _, role := range b.Roles {
			if !p.hasRole(role) {
				return fmt.Errorf("security: policy binding %q: unknown role %q", b.Subject, role)
			}
		}
		p.Bind(b.Subject, b.Roles...)
	}
	return nil
}
