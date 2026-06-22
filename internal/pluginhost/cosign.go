package pluginhost

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io"
	"os"
	"strings"
)

// PublicKeyEnv names the env var holding the path to a cosign public key (PEM).
// When set, every plugin binary must carry a valid signature in addition to its
// sha256 pin — provenance on top of integrity. Unset means signature
// verification is off (sha256 pinning still applies).
const PublicKeyEnv = "ROLLOPS_PLUGIN_PUBLIC_KEY"

// VerifyArtifact verifies a plugin binary's integrity (sha256 pin, always) and,
// when ROLLOPS_PLUGIN_PUBLIC_KEY is set, its provenance (a cosign key-based
// signature). The signature is read from "<path>.sig" — the default cosign
// --output-signature convention — as base64 over the binary's contents.
func VerifyArtifact(path, sha256hex string) error {
	if err := VerifyBinary(path, sha256hex); err != nil {
		return err
	}
	keyPath := os.Getenv(PublicKeyEnv)
	if keyPath == "" {
		return nil // signing not enabled
	}
	pubPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return fmt.Errorf("plugin: read public key (%s): %w", PublicKeyEnv, err)
	}
	sigPath := path + ".sig"
	sigB64, err := os.ReadFile(sigPath)
	if err != nil {
		return fmt.Errorf("plugin: %s set but signature %s missing: %w", PublicKeyEnv, sigPath, err)
	}
	return verifyCosignSignature(path, sigB64, pubPEM)
}

// verifyCosignSignature checks a cosign key-based blob signature (base64 of the
// raw signature bytes) over the file's contents against a PEM public key. cosign
// signs the sha256 of the payload; the signature scheme follows the key type
// (ECDSA-P256 ASN.1, Ed25519, or RSA PKCS#1v15), so it verifies with stdlib —
// no sigstore dependency, keeping the single static binary intact.
func verifyCosignSignature(path string, sigB64, pubPEM []byte) error {
	pub, err := parsePublicKey(pubPEM)
	if err != nil {
		return err
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sigB64)))
	if err != nil {
		return fmt.Errorf("plugin: decode signature: %w", err)
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("plugin: open binary: %w", err)
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("plugin: hash binary: %w", err)
	}
	digest := h.Sum(nil)

	switch k := pub.(type) {
	case *ecdsa.PublicKey:
		if !ecdsa.VerifyASN1(k, digest, sig) {
			return fmt.Errorf("plugin: %s: signature verification failed (ecdsa)", path)
		}
	case ed25519.PublicKey:
		// Ed25519 signs the message, not a pre-hash; cosign signs the payload.
		f2, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("plugin: open binary: %w", err)
		}
		defer func() { _ = f2.Close() }()
		content, err := io.ReadAll(f2)
		if err != nil {
			return fmt.Errorf("plugin: read binary: %w", err)
		}
		if !ed25519.Verify(k, content, sig) {
			return fmt.Errorf("plugin: %s: signature verification failed (ed25519)", path)
		}
	case *rsa.PublicKey:
		if err := rsa.VerifyPKCS1v15(k, crypto.SHA256, digest, sig); err != nil {
			return fmt.Errorf("plugin: %s: signature verification failed (rsa): %w", path, err)
		}
	default:
		return fmt.Errorf("plugin: unsupported public key type %T", pub)
	}
	return nil
}

func parsePublicKey(pubPEM []byte) (any, error) {
	block, _ := pem.Decode(pubPEM)
	if block == nil {
		return nil, fmt.Errorf("plugin: no PEM block in public key")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("plugin: parse public key: %w", err)
	}
	return pub, nil
}
