package security

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Confinement is the daemon-level multi-tenant confinement policy.
//
// In the documented "one repo per customer" model the repo config is untrusted:
// it can name commands to execute on the daemon host (smoke tests, database
// migrate/rollback hooks) and it can name a Kubernetes namespace, kubeconfig,
// and context. Left unconstrained, a poisoned tenant repo gets arbitrary command
// execution on the daemon host and can act outside its namespace / on another
// cluster.
//
// Every control here is OPT-IN and default-off so the common single-tenant /
// trusted-repo deployment keeps today's behavior with no configuration. When a
// control is configured it is enforced fail-closed. Controls are wired from the
// daemon environment via ConfinementFromEnv:
//
//	ROLLOPS_ALLOWED_COMMANDS       comma-separated command allowlist (basename or absolute path)
//	ROLLOPS_ALLOWED_NAMESPACES     comma-separated Kubernetes namespace allowlist
//	ROLLOPS_CONFINE_TARGET_CLUSTER "1"/"true"/"yes"/"on" ignores repo kubeconfig/context
type Confinement struct {
	commands       allowSet // config-sourced command allowlist
	namespaces     allowSet // Kubernetes namespace allowlist
	confineCluster bool     // ignore repo-supplied kubeconfig/context
}

// allowSet is an allowlist of permitted values. When enabled is false the set is
// "unset" and every value is permitted (today's behavior); when enabled is true
// only members of values are permitted (fail-closed).
type allowSet struct {
	enabled bool
	values  map[string]struct{}
}

// ConfinementFromEnv resolves the confinement policy from the environment. The
// getenv seam keeps it testable without mutating the process environment.
func ConfinementFromEnv(getenv func(string) string) Confinement {
	return Confinement{
		commands:       parseAllowSet(getenv("ROLLOPS_ALLOWED_COMMANDS")),
		namespaces:     parseAllowSet(getenv("ROLLOPS_ALLOWED_NAMESPACES")),
		confineCluster: truthy(getenv("ROLLOPS_CONFINE_TARGET_CLUSTER")),
	}
}

func parseAllowSet(raw string) allowSet {
	if strings.TrimSpace(raw) == "" {
		return allowSet{} // unset: no confinement
	}
	// A non-empty setting enables the allowlist. If it parses to zero valid
	// entries (e.g. ","), the set rejects everything — the fail-closed direction.
	values := make(map[string]struct{})
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			values[p] = struct{}{}
		}
	}
	return allowSet{enabled: true, values: values}
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// CommandsEnabled reports whether the command allowlist is active.
func (c Confinement) CommandsEnabled() bool { return c.commands.enabled }

// NamespacesEnabled reports whether the namespace allowlist is active.
func (c Confinement) NamespacesEnabled() bool { return c.namespaces.enabled }

// ClusterConfined reports whether repo-supplied kubeconfig/context are ignored.
func (c Confinement) ClusterConfined() bool { return c.confineCluster }

// Active reports whether any confinement control is engaged.
func (c Confinement) Active() bool {
	return c.commands.enabled || c.namespaces.enabled || c.confineCluster
}

// LogSummary renders the policy for a startup log line, e.g.
// "commands=on namespaces=off cluster=off".
func (c Confinement) LogSummary() string {
	return fmt.Sprintf("commands=%s namespaces=%s cluster=%s",
		onOff(c.commands.enabled), onOff(c.namespaces.enabled), onOff(c.confineCluster))
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// CheckCommand rejects a config-sourced command whose executable (command[0]) is
// not on the ROLLOPS_ALLOWED_COMMANDS allowlist. An empty command is a no-op (no
// hook to run). When the allowlist is unset it always passes.
//
// Allowlist entries match either by basename or absolute path: a bare-name entry
// ("psql") permits both "psql" and "/usr/bin/psql"; a path entry ("/usr/bin/psql")
// permits only that exact path.
func (c Confinement) CheckCommand(command []string) error {
	if !c.commands.enabled || len(command) == 0 {
		return nil
	}
	exe := command[0]
	if c.commands.allowsCommand(exe) {
		return nil
	}
	return fmt.Errorf("command %q is not allow-listed (ROLLOPS_ALLOWED_COMMANDS)", exe)
}

func (s allowSet) allowsCommand(exe string) bool {
	if _, ok := s.values[exe]; ok {
		return true // exact match: path==path or basename==basename
	}
	// A bare-name allowlist entry also matches an absolute path with that base.
	if _, ok := s.values[filepath.Base(exe)]; ok {
		return true
	}
	return false
}

// CheckNamespace rejects a resolved namespace that is not on the
// ROLLOPS_ALLOWED_NAMESPACES allowlist. When the allowlist is unset it always
// passes. An empty namespace resolves to "default" (matching kubectl).
func (c Confinement) CheckNamespace(namespace string) error {
	if !c.namespaces.enabled {
		return nil
	}
	if namespace == "" {
		namespace = "default"
	}
	if _, ok := c.namespaces.values[namespace]; ok {
		return nil
	}
	return fmt.Errorf("namespace %q is not allow-listed (ROLLOPS_ALLOWED_NAMESPACES)", namespace)
}
