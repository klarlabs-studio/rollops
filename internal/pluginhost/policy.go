package pluginhost

import (
	"fmt"
	"strings"

	pub "go.klarlabs.de/rollops/pkg/plugin"
)

// Policy bounds what a plugin's declared safety requirements may demand. A
// plugin whose manifest exceeds the policy is rejected before any tool runs —
// capability-scoped trust rather than full daemon trust.
type Policy struct {
	AllowedNetworkHosts []string      // exact host:port (or host) the plugin may declare; "*" allows any
	AllowedEnvVars      []string      // env var names the plugin may declare; "*" allows any
	AllowedFilePaths    []string      // path prefixes the plugin may declare; "*" allows any
	MaxRiskClass        pub.RiskClass // highest risk class admitted
	AllowConfirmation   bool          // admit plugins that declare needs_confirmation
}

// DefaultPolicy is a conservative baseline: the local filesystem and process
// environment are open (a plugin is a local binary the operator installed), but
// network egress must be explicitly declared and allow-listed, and only active
// risk is admitted. Operators tighten or loosen via config.
func DefaultPolicy() Policy {
	return Policy{
		AllowedNetworkHosts: nil, // none by default — declare + allow-list to enable
		AllowedEnvVars:      []string{"*"},
		AllowedFilePaths:    []string{"*"},
		MaxRiskClass:        pub.RiskActive,
		AllowConfirmation:   true,
	}
}

var riskRank = map[pub.RiskClass]int{pub.RiskPassive: 0, pub.RiskActive: 1, pub.RiskInvasive: 2}

// Validate rejects a manifest whose safety requirements exceed the policy.
func (p Policy) Validate(m pub.Manifest) error {
	s := m.Safety
	if rank, ok := riskRank[s.RiskClass]; s.RiskClass != "" && (!ok || rank > riskRank[p.MaxRiskClass]) {
		return fmt.Errorf("plugin %q: risk class %q exceeds policy max %q", m.Name, s.RiskClass, p.MaxRiskClass)
	}
	if s.NeedsConfirmation && !p.AllowConfirmation {
		return fmt.Errorf("plugin %q: requires confirmation, not permitted by policy", m.Name)
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
