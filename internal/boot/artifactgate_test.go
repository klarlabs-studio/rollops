package boot

import (
	"strings"
	"testing"

	"go.klarlabs.de/rollops/internal/security"
)

// gateFor builds the artifact policy from a fixed environment.
func gateFor(pairs map[string]string) (*security.ArtifactGate, string) {
	c := Config{Getenv: func(k string) string { return pairs[k] }}
	return c.artifactGate()
}

func TestNoGateWithoutConfiguration(t *testing.T) {
	gate, _ := gateFor(nil)

	// Verification stays opt-in: a deployment that never asked for it must not
	// start failing on upgrade.
	if gate != nil {
		t.Errorf("gate = %+v, want none", gate)
	}
}

func TestASignatureKeyAloneKeepsTodaysBehaviour(t *testing.T) {
	gate, describe := gateFor(map[string]string{"ROLLOPS_COSIGN_KEY": "cosign.pub"})

	if gate == nil {
		t.Fatal("gate = nil")
	}
	if gate.Mode != security.VerifyEnforce {
		t.Errorf("Mode = %s, want enforce", gate.Mode)
	}
	chain, ok := gate.Verifier.(security.ChainVerifier)
	if !ok || len(chain.Verifiers) != 1 {
		t.Fatalf("Verifier = %#v, want a single signature check", gate.Verifier)
	}
	if !strings.Contains(describe, "signature") || strings.Contains(describe, "provenance") {
		t.Errorf("describe = %q", describe)
	}
}

func TestProvenanceLayersOnTopOfTheSignature(t *testing.T) {
	gate, describe := gateFor(map[string]string{
		"ROLLOPS_COSIGN_KEY":          "cosign.pub",
		"ROLLOPS_PROVENANCE_BUILDERS": "https://github.com/klarlabs-studio/kiln@",
	})

	chain, ok := gate.Verifier.(security.ChainVerifier)
	if !ok || len(chain.Verifiers) != 2 {
		t.Fatalf("Verifier = %#v, want both checks", gate.Verifier)
	}
	// Both, not one instead of the other: they answer different questions.
	if !strings.Contains(describe, "signature") || !strings.Contains(describe, "provenance") {
		t.Errorf("describe = %q, want both named", describe)
	}
}

func TestProvenanceWithoutAKeyIsRefusedNotSilentlyBroken(t *testing.T) {
	gate, _ := gateFor(map[string]string{
		"ROLLOPS_PROVENANCE_BUILDERS": "https://github.com/klarlabs-studio/kiln@",
	})

	// A provenance verifier with nothing to authenticate against would fail
	// every deploy, which reads as a broken pipeline rather than the
	// misconfiguration it is.
	if gate != nil {
		t.Errorf("gate = %+v, want none until a key or identity is set", gate)
	}
}

func TestKeylessIdentityConfiguresBoth(t *testing.T) {
	gate, describe := gateFor(map[string]string{
		"ROLLOPS_COSIGN_IDENTITY":     "https://github.com/klarlabs-studio/kiln/.github/workflows/ci.yml@refs/heads/main",
		"ROLLOPS_COSIGN_ISSUER":       "https://token.actions.githubusercontent.com",
		"ROLLOPS_PROVENANCE_BUILDERS": "https://github.com/klarlabs-studio/kiln@",
	})

	chain, ok := gate.Verifier.(security.ChainVerifier)
	if !ok || len(chain.Verifiers) != 2 {
		t.Fatalf("keyless did not configure both checks: %#v", gate.Verifier)
	}
	if !strings.Contains(describe, "provenance") {
		t.Errorf("describe = %q", describe)
	}
}

func TestRequireReprovedIsPassedThrough(t *testing.T) {
	gate, _ := gateFor(map[string]string{
		"ROLLOPS_COSIGN_KEY":                  "cosign.pub",
		"ROLLOPS_PROVENANCE_BUILDERS":         "https://github.com/klarlabs-studio/kiln@",
		"ROLLOPS_PROVENANCE_REQUIRE_REPROVED": "true",
	})

	chain := gate.Verifier.(security.ChainVerifier)
	prov, ok := chain.Verifiers[1].(security.ProvenanceVerifier)
	if !ok {
		t.Fatalf("second verifier = %#v", chain.Verifiers[1])
	}
	if !prov.RequireReproved {
		t.Error("RequireReproved did not reach the verifier")
	}
}

func TestSeveralAllowedBuilders(t *testing.T) {
	gate, _ := gateFor(map[string]string{
		"ROLLOPS_COSIGN_KEY": "cosign.pub",
		// A fleet migrating between builders needs both accepted at once.
		"ROLLOPS_PROVENANCE_BUILDERS": "https://github.com/klarlabs-studio/kiln@, https://github.com/slsa-framework/slsa-github-generator@",
	})

	chain := gate.Verifier.(security.ChainVerifier)
	prov := chain.Verifiers[1].(security.ProvenanceVerifier)
	if len(prov.AllowedBuilders) != 2 {
		t.Errorf("AllowedBuilders = %v, want both", prov.AllowedBuilders)
	}
}

func TestTheSourceGateJoinsAgainstTheProvenance(t *testing.T) {
	gate, describe := gateFor(map[string]string{
		"ROLLOPS_COSIGN_KEY":          "cosign.pub",
		"ROLLOPS_PROVENANCE_BUILDERS": "https://github.com/klarlabs-studio/kiln@",
		"ROLLOPS_SOURCE_GATES":        "https://warden.klarlabs.de",
	})

	chain := gate.Verifier.(security.ChainVerifier)
	if len(chain.Verifiers) != 3 {
		t.Fatalf("want signature, provenance and source gate: %#v", chain.Verifiers)
	}
	src, ok := chain.Verifiers[2].(security.SourceGateVerifier)
	if !ok {
		t.Fatalf("third verifier = %#v", chain.Verifiers[2])
	}
	// Without the provenance verifier the source gate cannot check that the
	// commit warden vouched for is the commit that was built.
	if src.Provenance == nil {
		t.Error("the source gate has nothing to join against")
	}
	if !strings.Contains(describe, "source gated by") {
		t.Errorf("describe = %q", describe)
	}
}

func TestASourceGateWithoutAKeyIsRefused(t *testing.T) {
	gate, _ := gateFor(map[string]string{
		"ROLLOPS_SOURCE_GATES": "https://warden.klarlabs.de",
	})

	if gate != nil {
		t.Errorf("gate = %+v, want none until a key or identity is set", gate)
	}
}

func TestRequiredSourceLevelsReachTheVerifier(t *testing.T) {
	gate, _ := gateFor(map[string]string{
		"ROLLOPS_COSIGN_KEY":            "cosign.pub",
		"ROLLOPS_SOURCE_GATES":          "https://warden.klarlabs.de",
		"ROLLOPS_SOURCE_REQUIRE_LEVELS": "WARDEN_SOURCE_SIGNED",
	})

	chain := gate.Verifier.(security.ChainVerifier)
	src := chain.Verifiers[1].(security.SourceGateVerifier)
	if len(src.RequireLevels) != 1 || src.RequireLevels[0] != "WARDEN_SOURCE_SIGNED" {
		t.Errorf("RequireLevels = %v", src.RequireLevels)
	}
}
