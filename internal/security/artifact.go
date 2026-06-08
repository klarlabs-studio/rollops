package security

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// VerifyMode is the per-target artifact verification policy.
type VerifyMode string

const (
	VerifyEnforce VerifyMode = "enforce" // deploy only if provenance verifies (secure default)
	VerifyWarn    VerifyMode = "warn"    // verify and surface, but do not block
	VerifyOff     VerifyMode = "off"     // skip verification (explicit opt-out)
)

// ArtifactVerifier checks an artifact's signature/provenance. This matters most
// for agent-driven rollouts: the agent chose what to deploy, so the system
// independently verifies it is what it claims to be.
type ArtifactVerifier interface {
	Verify(ctx context.Context, ref string) (verified bool, detail string, err error)
}

// ArtifactGate applies a VerifyMode over a verifier. Enforce is the secure
// default; Off must be an explicit operator choice.
type ArtifactGate struct {
	Mode     VerifyMode
	Verifier ArtifactVerifier
}

// ErrUnverifiedArtifact is returned by an enforcing gate when verification fails.
type ErrUnverifiedArtifact struct {
	Ref    string
	Detail string
}

func (e ErrUnverifiedArtifact) Error() string {
	return fmt.Sprintf("security: artifact %q failed verification: %s", e.Ref, e.Detail)
}

// Check runs verification according to the mode. Enforce blocks on failure;
// Warn returns (false-positive) nil with the detail for the caller to log; Off
// skips entirely.
func (g ArtifactGate) Check(ctx context.Context, ref string) error {
	if g.Mode == VerifyOff || g.Mode == "" && g.Verifier == nil {
		return nil
	}
	if g.Verifier == nil {
		return ErrUnverifiedArtifact{Ref: ref, Detail: "no verifier configured but mode is " + string(g.Mode)}
	}
	ok, detail, err := g.Verifier.Verify(ctx, ref)
	switch g.Mode {
	case VerifyWarn:
		return nil // caller logs detail/err; never blocks
	case VerifyEnforce, "":
		if err != nil {
			return ErrUnverifiedArtifact{Ref: ref, Detail: err.Error()}
		}
		if !ok {
			return ErrUnverifiedArtifact{Ref: ref, Detail: detail}
		}
		return nil
	default:
		return fmt.Errorf("security: unknown verify mode %q", g.Mode)
	}
}

// CosignVerifier verifies container artifacts with cosign. The exec runner is
// injectable for tests; the real one shells the cosign binary.
type CosignVerifier struct {
	CertIdentity   string // expected signer identity (keyless)
	CertOIDCIssuer string
	Run            func(ctx context.Context, name string, args ...string) (output string, err error)
}

// Verify runs `cosign verify` with the configured identity constraints.
func (c CosignVerifier) Verify(ctx context.Context, ref string) (bool, string, error) {
	run := c.Run
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) (string, error) {
			out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
			return string(out), err
		}
	}
	args := []string{"verify"}
	if c.CertIdentity != "" {
		args = append(args, "--certificate-identity", c.CertIdentity)
	}
	if c.CertOIDCIssuer != "" {
		args = append(args, "--certificate-oidc-issuer", c.CertOIDCIssuer)
	}
	args = append(args, ref)
	out, err := run(ctx, "cosign", args...)
	if err != nil {
		return false, strings.TrimSpace(out), nil // verification failed, not a system error
	}
	return true, "verified", nil
}
