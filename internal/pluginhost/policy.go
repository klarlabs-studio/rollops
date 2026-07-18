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

// AllowedEnvEnv names the env var that allow-lists which host environment
// variables a plugin subprocess may inherit: a comma-separated list of variable
// names, or "*" to inherit the daemon's full environment. Unset means deny —
// the plugin sees only a minimal essential env (see security.EssentialEnvVars),
// never the daemon's cloud credentials, tokens, or other secrets.
const AllowedEnvEnv = "ROLLOPS_PLUGIN_ALLOWED_ENV"

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

// DefaultPolicy is a conservative baseline: the local filesystem is open (a
// plugin is a local binary the operator installed), but network egress and
// environment inheritance must be explicitly declared and allow-listed, and only
// active risk is admitted. The plugin's environment is confined to a minimal
// essential set unless the operator names variables in ROLLOPS_PLUGIN_ALLOWED_ENV
// (or "*" to inherit everything) — so the daemon's secrets are not handed to a
// plugin by default. A plugin that declares needs_confirmation must additionally
// be named in ROLLOPS_PLUGIN_CONFIRM. Operators tighten or loosen via config.
func DefaultPolicy() Policy {
	return Policy{
		AllowedNetworkHosts: nil,                                 // none by default — declare + allow-list to enable
		AllowedEnvVars:      splitList(os.Getenv(AllowedEnvEnv)), // deny by default — allow-list to enable
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

// riskRankOf ranks a risk class for comparison. An unset class ranks below any
// declared one; an unrecognised (unknown) class ranks above every known one, so
// an unknown per-tool risk fails closed — it surfaces as the effective risk and
// is rejected rather than silently admitted as passive.
func riskRankOf(c pub.RiskClass) int {
	switch c {
	case "":
		return -1
	case pub.RiskPassive:
		return 0
	case pub.RiskActive:
		return 1
	case pub.RiskInvasive:
		return 2
	default:
		return 3 // unknown declared class: above all known classes
	}
}

// effectiveRisk is the plugin-wide risk class when declared, else the highest
// risk class across the manifest's tools (unset when none declare one). An
// unknown per-tool class outranks the known ones so it is carried up to Validate
// and rejected.
func effectiveRisk(m pub.Manifest) pub.RiskClass {
	if m.Safety.RiskClass != "" {
		return m.Safety.RiskClass
	}
	best := pub.RiskClass("")
	for _, c := range m.Capabilities {
		for _, t := range c.Tools {
			if t.RiskClass != "" && riskRankOf(t.RiskClass) > riskRankOf(best) {
				best = t.RiskClass
			}
		}
	}
	return best
}

// Validate rejects a manifest whose safety requirements exceed the policy.
func (p Policy) Validate(m pub.Manifest) error {
	s := m.Safety
	// Effective risk is the plugin-wide class when set, else the highest per-tool
	// class — so a plugin that rates a tool invasive but omits a plugin-wide class
	// is still caught.
	eff := effectiveRisk(m)
	if rank, ok := riskRank[eff]; eff != "" && (!ok || rank > riskRank[p.MaxRiskClass]) {
		return fmt.Errorf("plugin %q: risk class %q exceeds policy max %q", m.Name, eff, p.MaxRiskClass)
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
