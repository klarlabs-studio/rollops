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

func TestHasCapability(t *testing.T) {
	m := pub.Manifest{Capabilities: []pub.Capability{{Name: pub.CapabilityTarget}}}
	if !HasCapability(m, pub.CapabilityTarget) {
		t.Error("declared capability must be found")
	}
	if HasCapability(m, pub.CapabilityFeatureFlag) {
		t.Error("undeclared capability must not be found")
	}
}
