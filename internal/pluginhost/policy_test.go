package pluginhost

import (
	"testing"

	pub "go.klarlabs.de/rollops/pkg/plugin"
)

func TestPolicy_Validate(t *testing.T) {
	man := func(s pub.Safety) pub.Manifest { return pub.Manifest{Name: "p", Safety: s} }

	// Default policy: network must be allow-listed, active risk ok.
	p := DefaultPolicy()
	if err := p.Validate(man(pub.Safety{RiskClass: pub.RiskActive})); err != nil {
		t.Errorf("active risk, no network → ok, got %v", err)
	}
	if err := p.Validate(man(pub.Safety{NetworkHosts: []string{"api:443"}})); err == nil {
		t.Error("undeclared-allowed network host must be rejected by default")
	}
	if err := p.Validate(man(pub.Safety{RiskClass: pub.RiskInvasive})); err == nil {
		t.Error("invasive risk exceeds default max")
	}

	// Allow-listed network host passes.
	p.AllowedNetworkHosts = []string{"api:443"}
	if err := p.Validate(man(pub.Safety{NetworkHosts: []string{"api:443"}})); err != nil {
		t.Errorf("allow-listed host → ok, got %v", err)
	}
	if err := p.Validate(man(pub.Safety{NetworkHosts: []string{"evil:443"}})); err == nil {
		t.Error("non-allow-listed host must be rejected")
	}

	// File path prefix matching.
	p.AllowedFilePaths = []string{"/etc/rollops/"}
	if err := p.Validate(man(pub.Safety{FilePaths: []string{"/etc/rollops/x.yaml"}})); err != nil {
		t.Errorf("prefix match → ok, got %v", err)
	}
	if err := p.Validate(man(pub.Safety{FilePaths: []string{"/secret"}})); err == nil {
		t.Error("path outside prefix must be rejected")
	}
}

func TestPolicy_EffectiveRiskFromTools(t *testing.T) {
	// No plugin-wide risk, but a tool is invasive → effective risk is invasive,
	// which exceeds the default max (active) and is rejected.
	m := pub.Manifest{
		Name: "p",
		Capabilities: []pub.Capability{{
			Name: pub.CapabilityTarget,
			Tools: []pub.Tool{
				{Name: "observe", RiskClass: pub.RiskPassive},
				{Name: "apply", Mutating: true, RiskClass: pub.RiskInvasive},
			},
		}},
	}
	if err := DefaultPolicy().Validate(m); err == nil {
		t.Error("invasive per-tool risk must be caught even without a plugin-wide class")
	}
	// All tools active → effective active → admitted by default.
	m.Capabilities[0].Tools[1].RiskClass = pub.RiskActive
	if err := DefaultPolicy().Validate(m); err != nil {
		t.Errorf("active effective risk → ok, got %v", err)
	}
	// Plugin-wide class wins over per-tool when set.
	m.Safety.RiskClass = pub.RiskPassive
	m.Capabilities[0].Tools[1].RiskClass = pub.RiskInvasive
	if err := DefaultPolicy().Validate(m); err != nil {
		t.Errorf("explicit plugin-wide passive overrides per-tool, got %v", err)
	}
}

func TestPolicy_NeedsConfirmation(t *testing.T) {
	man := pub.Manifest{Name: "acme/dangerous", Safety: pub.Safety{NeedsConfirmation: true}}

	// Not confirmed → rejected, even though AllowConfirmation is true by default.
	p := DefaultPolicy()
	p.ConfirmedPlugins = nil
	if err := p.Validate(man); err == nil {
		t.Error("unconfirmed needs_confirmation plugin must be rejected")
	}
	// Confirmed by name → ok.
	p.ConfirmedPlugins = []string{"acme/dangerous"}
	if err := p.Validate(man); err != nil {
		t.Errorf("confirmed plugin → ok, got %v", err)
	}
	// Wildcard confirms any.
	p.ConfirmedPlugins = []string{"*"}
	if err := p.Validate(man); err != nil {
		t.Errorf("wildcard confirm → ok, got %v", err)
	}
	// Master switch off → rejected regardless of confirmation list.
	p.AllowConfirmation = false
	if err := p.Validate(man); err == nil {
		t.Error("AllowConfirmation=false must reject needs_confirmation plugin")
	}
	// A plugin that does not need confirmation is unaffected.
	p = DefaultPolicy()
	p.ConfirmedPlugins = nil
	if err := p.Validate(pub.Manifest{Name: "plain"}); err != nil {
		t.Errorf("non-confirmation plugin → ok, got %v", err)
	}
}

func TestDefaultPolicy_ConfirmFromEnv(t *testing.T) {
	t.Setenv(ConfirmEnv, " a/b , c/d ")
	got := DefaultPolicy().ConfirmedPlugins
	if len(got) != 2 || got[0] != "a/b" || got[1] != "c/d" {
		t.Fatalf("ConfirmedPlugins from env = %v, want [a/b c/d]", got)
	}
	t.Setenv(ConfirmEnv, "")
	if got := DefaultPolicy().ConfirmedPlugins; len(got) != 0 {
		t.Fatalf("empty env → no confirmed plugins, got %v", got)
	}
}

func TestHasCapability(t *testing.T) {
	m := pub.Manifest{Capabilities: []pub.Capability{{Name: pub.CapabilityTarget}}}
	if !HasCapability(m, pub.CapabilityTarget) {
		t.Error("declared capability must be found")
	}
	if HasCapability(m, pub.CapabilityFeatureFlag) {
		t.Error("undeclared capability must not be found")
	}
}
