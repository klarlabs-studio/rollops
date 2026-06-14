package pluginhost

import (
	"fmt"
	"os"
	"strings"

	pub "go.klarlabs.de/rollops/pkg/plugin"
)

// ConfirmEnv names the env var that confirms needs_confirmation plugins: a
// comma-separated list of plugin names, or "*" to confirm any. A daemon can't
// prompt interactively, so confirmation is an explicit, audited operator opt-in
// at load time — a plugin that declares needs_confirmation will not load unless
// its name is listed here.
const ConfirmEnv = "ROLLOPS_PLUGIN_CONFIRM"

// Policy bounds what a plugin's declared safety requirements may demand. A
// plugin whose manifest exceeds the policy is rejected before any tool runs —
// capability-scoped trust rather than full daemon trust.
type Policy struct {
	AllowedNetworkHosts []string      // exact host:port (or host) the plugin may declare; "*" allows any
	AllowedEnvVars      []string      // env var names the plugin may declare; "*" allows any
	AllowedFilePaths    []string      // path prefixes the plugin may declare; "*" allows any
	MaxRiskClass        pub.RiskClass // highest risk class admitted
	AllowConfirmation   bool          // admit plugins that declare needs_confirmation at all
	ConfirmedPlugins    []string      // names the operator confirmed; "*" confirms any
}

// DefaultPolicy is a conservative baseline: the local filesystem and process
// environment are open (a plugin is a local binary the operator installed), but
// network egress must be explicitly declared and allow-listed, and only active
// risk is admitted. A plugin that declares needs_confirmation must additionally
// be named in ROLLOPS_PLUGIN_CONFIRM. Operators tighten or loosen via config.
func DefaultPolicy() Policy {
	return Policy{
		AllowedNetworkHosts: nil, // none by default — declare + allow-list to enable
		AllowedEnvVars:      []string{"*"},
		AllowedFilePaths:    []string{"*"},
		MaxRiskClass:        pub.RiskActive,
		AllowConfirmation:   true,
		ConfirmedPlugins:    splitList(os.Getenv(ConfirmEnv)),
	}
}

// splitList parses a comma-separated env value into trimmed, non-empty entries.
func splitList(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

var riskRank = map[pub.RiskClass]int{pub.RiskPassive: 0, pub.RiskActive: 1, pub.RiskInvasive: 2}

// Validate rejects a manifest whose safety requirements exceed the policy.
func (p Policy) Validate(m pub.Manifest) error {
	s := m.Safety
	if rank, ok := riskRank[s.RiskClass]; s.RiskClass != "" && (!ok || rank > riskRank[p.MaxRiskClass]) {
		return fmt.Errorf("plugin %q: risk class %q exceeds policy max %q", m.Name, s.RiskClass, p.MaxRiskClass)
	}
	if s.NeedsConfirmation {
		if !p.AllowConfirmation {
			return fmt.Errorf("plugin %q: requires confirmation, not permitted by policy", m.Name)
		}
		if !allowed(m.Name, p.ConfirmedPlugins) {
			return fmt.Errorf("plugin %q: requires operator confirmation; add it to %s (or set %q to \"*\") to load it", m.Name, ConfirmEnv, ConfirmEnv)
		}
	}
	for _, h := range s.NetworkHosts {
		if !allowed(h, p.AllowedNetworkHosts) {
			return fmt.Errorf("plugin %q: network host %q not allow-listed", m.Name, h)
		}
	}
	for _, e := range s.EnvVars {
		if !allowed(e, p.AllowedEnvVars) {
			return fmt.Errorf("plugin %q: env var %q not allow-listed", m.Name, e)
		}
	}
	for _, f := range s.FilePaths {
		if !prefixAllowed(f, p.AllowedFilePaths) {
			return fmt.Errorf("plugin %q: file path %q not allow-listed", m.Name, f)
		}
	}
	return nil
}

func allowed(v string, list []string) bool {
	for _, a := range list {
		if a == "*" || a == v {
			return true
		}
	}
	return false
}

func prefixAllowed(v string, list []string) bool {
	for _, a := range list {
		if a == "*" || strings.HasPrefix(v, a) {
			return true
		}
	}
	return false
}
