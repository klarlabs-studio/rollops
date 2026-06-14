package pluginhost

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

// signedArtifact writes a fake plugin binary, its detached signature, and the
// public key PEM into dir, returning their paths and the binary's sha256.
func signedArtifact(t *testing.T, dir string, sign func(digest, content []byte) []byte, pubPEM []byte) (bin, sha string) {
	t.Helper()
	bin = filepath.Join(dir, "plugin")
	content := []byte("#!/bin/sh\necho fake plugin\n")
	if err := os.WriteFile(bin, content, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	sig := sign(sum[:], content)
	if err := os.WriteFile(bin+".sig", []byte(base64.StdEncoding.EncodeToString(sig)), 0o644); err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(dir, "cosign.pub")
	if err := os.WriteFile(keyFile, pubPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	hexSum := make([]byte, 0)
	for _, b := range sum {
		const hexdig = "0123456789abcdef"
		hexSum = append(hexSum, hexdig[b>>4], hexdig[b&0x0f])
	}
	t.Setenv(PublicKeyEnv, keyFile)
	return bin, string(hexSum)
}

func ecdsaSigner(t *testing.T) (func(digest, content []byte) []byte, []byte) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	return func(digest, _ []byte) []byte {
		sig, err := ecdsa.SignASN1(rand.Reader, key, digest)
		if err != nil {
			t.Fatal(err)
		}
		return sig
	}, pubPEM
}

func TestVerifyArtifact_ECDSASignature(t *testing.T) {
	dir := t.TempDir()
	sign, pub := ecdsaSigner(t)
	bin, sha := signedArtifact(t, dir, sign, pub)
	if err := VerifyArtifact(bin, sha); err != nil {
		t.Fatalf("valid ECDSA signature must verify: %v", err)
	}
}

func TestVerifyArtifact_Ed25519Signature(t *testing.T) {
	dir := t.TempDir()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(pub)
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	bin, sha := signedArtifact(t, dir, func(_, content []byte) []byte {
		return ed25519.Sign(priv, content)
	}, pubPEM)
	if err := VerifyArtifact(bin, sha); err != nil {
		t.Fatalf("valid Ed25519 signature must verify: %v", err)
	}
}

func TestVerifyArtifact_TamperedSignatureRejected(t *testing.T) {
	dir := t.TempDir()
	sign, pub := ecdsaSigner(t)
	bin, sha := signedArtifact(t, dir, sign, pub)
	// Corrupt the signature file.
	if err := os.WriteFile(bin+".sig", []byte(base64.StdEncoding.EncodeToString([]byte("garbage-signature-bytes"))), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := VerifyArtifact(bin, sha); err == nil {
		t.Fatal("tampered signature must be rejected")
	}
}

func TestVerifyArtifact_WrongKeyRejected(t *testing.T) {
	dir := t.TempDir()
	sign, _ := ecdsaSigner(t)
	_, otherPub := ecdsaSigner(t) // a different key's public part
	bin, sha := signedArtifact(t, dir, sign, otherPub)
	if err := VerifyArtifact(bin, sha); err == nil {
		t.Fatal("signature from a different key must be rejected")
	}
}

func TestVerifyArtifact_MissingSignatureWhenRequired(t *testing.T) {
	dir := t.TempDir()
	sign, pub := ecdsaSigner(t)
	bin, sha := signedArtifact(t, dir, sign, pub)
	if err := os.Remove(bin + ".sig"); err != nil {
		t.Fatal(err)
	}
	if err := VerifyArtifact(bin, sha); err == nil {
		t.Fatal("missing signature with key configured must be rejected")
	}
}

func TestVerifyArtifact_NoKeyConfiguredSkipsSignature(t *testing.T) {
	// No ROLLOPS_PLUGIN_PUBLIC_KEY → only the sha256 pin is enforced.
	t.Setenv(PublicKeyEnv, "")
	dir := t.TempDir()
	bin := filepath.Join(dir, "plugin")
	content := []byte("plain binary")
	if err := os.WriteFile(bin, content, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	hexSum := ""
	const hexdig = "0123456789abcdef"
	for _, b := range sum {
		hexSum += string([]byte{hexdig[b>>4], hexdig[b&0x0f]})
	}
	if err := VerifyArtifact(bin, hexSum); err != nil {
		t.Fatalf("no key configured → sha256-only must pass: %v", err)
	}
	// A wrong pin still fails (integrity always enforced).
	if err := VerifyArtifact(bin, "00"); err == nil {
		t.Fatal("wrong sha256 pin must always fail")
	}
}
