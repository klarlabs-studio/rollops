//go:build integration

package integration

import (
	"context"
	"os/exec"
	"testing"

	"go.klarlabs.de/rollops/internal/secrets"
	"go.klarlabs.de/rollops/internal/security"
)

// TestVaultProvider_Live resolves a secret from a real Vault dev server
// (docker-compose), exercising the KV v2 HTTP path end to end.
func TestVaultProvider_Live(t *testing.T) {
	addr := getenv("VAULT_ADDR", "")
	if addr == "" {
		t.Skip("VAULT_ADDR not set; run via test/integration/run.sh")
	}
	v := secrets.VaultProvider{Addr: addr, Token: getenv("VAULT_TOKEN", "roottoken")}

	s, err := v.Resolve(context.Background(), "secret/myapp#password")
	if err != nil {
		t.Fatalf("resolve from live vault: %v", err)
	}
	if s.Reveal() != "s3cr3t-live" {
		t.Errorf("vault returned %q, want s3cr3t-live", s.Reveal())
	}
	// Self-redaction holds on a live-resolved secret.
	if s.String() != "***" {
		t.Errorf("live secret did not redact: %q", s.String())
	}

	if _, err := v.Resolve(context.Background(), "secret/missing#password"); err == nil {
		t.Error("missing path should error")
	}
}

// TestCosignVerifier_Live verifies a real cosign-signed image in a local
// registry, and rejects an unsigned one — the artifact-provenance gate against
// real cosign.
func TestCosignVerifier_Live(t *testing.T) {
	key := getenv("COSIGN_PUB", "")
	signed := getenv("COSIGN_SIGNED_IMAGE", "")
	if key == "" || signed == "" {
		t.Skip("COSIGN_PUB/COSIGN_SIGNED_IMAGE not set; run via test/integration/run.sh")
	}
	if _, err := exec.LookPath("cosign"); err != nil {
		t.Skip("cosign not installed")
	}

	gate := security.ArtifactGate{
		Mode:     security.VerifyEnforce,
		Verifier: security.CosignVerifier{KeyPath: key, AllowHTTP: true}, // local http registry
	}
	if err := gate.Check(context.Background(), signed); err != nil {
		t.Fatalf("signed image should pass the enforcing gate: %v", err)
	}

	if unsigned := getenv("COSIGN_UNSIGNED_IMAGE", ""); unsigned != "" {
		if err := gate.Check(context.Background(), unsigned); err == nil {
			t.Error("unsigned image must be rejected by the enforcing gate")
		}
	}
}
