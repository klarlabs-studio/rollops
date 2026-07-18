package security

import "strings"

// EssentialEnvVars are always forwarded to a confined subprocess. They carry no
// daemon secrets, and a command that inherits nothing at all cannot resolve a
// binary or write a temp file.
var EssentialEnvVars = []string{"PATH", "HOME", "TMPDIR"}

// ConfineEnv computes the environment for a confined subprocess from an
// allow-list of variable names. The essential set is always included. A single
// "*" entry inherits the full parent environment — the explicit, only way to
// hand a subprocess the daemon's secrets. Otherwise only the essentials plus the
// named variables that are actually present are forwarded; the default (empty
// allow-list) is deny.
//
// This is the shared implementation behind both confined-subprocess paths: the
// plugin host, and config-sourced commands (smoke tests, database hooks) via
// Confinement.CommandEnv.
func ConfineEnv(allowed, parentEnv []string) []string {
	for _, a := range allowed {
		if strings.TrimSpace(a) == "*" {
			out := make([]string, len(parentEnv))
			copy(out, parentEnv)
			return out
		}
	}
	want := make(map[string]struct{}, len(EssentialEnvVars)+len(allowed))
	for _, k := range EssentialEnvVars {
		want[k] = struct{}{}
	}
	for _, k := range allowed {
		if k = strings.TrimSpace(k); k != "" {
			want[k] = struct{}{}
		}
	}
	out := make([]string, 0, len(want))
	for _, kv := range parentEnv {
		i := strings.IndexByte(kv, '=')
		if i < 0 {
			continue
		}
		if _, ok := want[kv[:i]]; ok {
			out = append(out, kv)
		}
	}
	return out
}

// CommandEnv returns the environment for a config-sourced command (a smoke test
// or a database migrate/rollback hook).
//
// These commands are named by the repo config, which this package treats as
// untrusted — so they do NOT inherit the daemon's environment. Without that
// scrub, a command from a watched repo could read the daemon's MCP and admin
// tokens, UI password, registry credentials and OIDC settings straight out of
// its own environment. Only the essential variables are forwarded, plus any the
// operator allow-lists via ROLLOPS_ALLOWED_ENV; "*" restores full inheritance.
//
// Unlike the other confinement controls this one is default-ON, matching the
// plugin host, which has always confined its subprocess environment.
func (c Confinement) CommandEnv(parentEnv []string) []string {
	return ConfineEnv(c.allowedEnv, parentEnv)
}

// EnvAllowList returns the operator-configured extra variable names, for the
// startup log and `doctor`.
func (c Confinement) EnvAllowList() []string { return c.allowedEnv }
