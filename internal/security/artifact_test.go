package security

import (
	"context"
	"errors"
	"testing"
)

type fakeVerifier struct {
	ok     bool
	detail string
	err    error
}

func (f fakeVerifier) Verify(context.Context, string) (bool, string, error) {
	return f.ok, f.detail, f.err
}

func TestArtifactGate_OffSkips(t *testing.T) {
	g := ArtifactGate{Mode: VerifyOff, Verifier: fakeVerifier{ok: false}}
	if err := g.Check(context.Background(), "img:bad"); err != nil {
		t.Errorf("off mode must skip: %v", err)
	}
}

func TestArtifactGate_EnforceAllowsVerified(t *testing.T) {
	g := ArtifactGate{Mode: VerifyEnforce, Verifier: fakeVerifier{ok: true}}
	if err := g.Check(context.Background(), "img:good"); err != nil {
		t.Errorf("verified artifact must pass: %v", err)
	}
}

func TestArtifactGate_EnforceBlocksUnverified(t *testing.T) {
	g := ArtifactGate{Mode: VerifyEnforce, Verifier: fakeVerifier{ok: false, detail: "no signature"}}
	err := g.Check(context.Background(), "img:bad")
	var ue ErrUnverifiedArtifact
	if !errors.As(err, &ue) {
		t.Fatalf("expected ErrUnverifiedArtifact, got %v", err)
	}
}

func TestArtifactGate_WarnNeverBlocks(t *testing.T) {
	g := ArtifactGate{Mode: VerifyWarn, Verifier: fakeVerifier{ok: false}}
	if err := g.Check(context.Background(), "img:bad"); err != nil {
		t.Errorf("warn mode must not block: %v", err)
	}
}

func TestCosignVerifier_BuildsArgsAndInterpretsExit(t *testing.T) {
	var gotArgs []string
	c := CosignVerifier{
		CertIdentity:   "ci@acme.dev",
		CertOIDCIssuer: "https://token.actions.githubusercontent.com",
		Run: func(_ context.Context, _ string, args ...string) (string, error) {
			gotArgs = args
			return "ok", nil
		},
	}
	ok, _, err := c.Verify(context.Background(), "ghcr.io/acme/api:1.2.3")
	if err != nil || !ok {
		t.Fatalf("verify ok=%v err=%v", ok, err)
	}
	joined := ""
	for _, a := range gotArgs {
		joined += a + " "
	}
	if !contains(joined, "--certificate-identity") || !contains(joined, "ghcr.io/acme/api:1.2.3") {
		t.Errorf("cosign args wrong: %v", gotArgs)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (indexOf(s, sub) >= 0)
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
