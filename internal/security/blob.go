package security

import (
	"context"
	"os/exec"
	"strings"
)

// CosignBlobVerifier verifies a file (a plugin binary) with `cosign
// verify-blob`. Key-based verification uses KeyPath; keyless uses
// CertIdentity + CertOIDCIssuer with a bundle or certificate. The exec runner
// is injectable for tests.
type CosignBlobVerifier struct {
	KeyPath         string // public key for key-based verify (--key)
	CertIdentity    string // expected signer identity (keyless)
	CertOIDCIssuer  string // expected OIDC issuer (keyless)
	SignaturePath   string // detached signature file (--signature)
	CertificatePath string // signing certificate for keyless (--certificate)
	BundlePath      string // sigstore bundle (--bundle), an alternative to sig+cert
	Run             func(ctx context.Context, name string, args ...string) (output string, err error)
}

// Configured reports whether any verification material was supplied; an
// unconfigured verifier verifies nothing and callers should treat that as "no
// signature check requested".
func (c CosignBlobVerifier) Configured() bool {
	return c.KeyPath != "" || c.CertIdentity != "" || c.BundlePath != "" || c.SignaturePath != ""
}

// VerifyBlob runs `cosign verify-blob` over path. It returns (true, "verified")
// on success and (false, reason) when cosign rejects the signature; a non-nil
// error is a system/exec failure.
func (c CosignBlobVerifier) VerifyBlob(ctx context.Context, path string) (bool, string, error) {
	run := c.Run
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) (string, error) {
			out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
			return string(out), err
		}
	}
	args := []string{"verify-blob"}
	if c.KeyPath != "" {
		args = append(args, "--key", c.KeyPath)
	}
	if c.CertIdentity != "" {
		args = append(args, "--certificate-identity", c.CertIdentity)
	}
	if c.CertOIDCIssuer != "" {
		args = append(args, "--certificate-oidc-issuer", c.CertOIDCIssuer)
	}
	if c.SignaturePath != "" {
		args = append(args, "--signature", c.SignaturePath)
	}
	if c.CertificatePath != "" {
		args = append(args, "--certificate", c.CertificatePath)
	}
	if c.BundlePath != "" {
		args = append(args, "--bundle", c.BundlePath)
	}
	args = append(args, path)
	out, err := run(ctx, "cosign", args...)
	if err != nil {
		return false, strings.TrimSpace(out), nil
	}
	return true, "verified", nil
}
