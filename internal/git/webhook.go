// Package git is the Git integration: HMAC-verified webhooks for immediate
// reconciliation, periodic poll as the safety net (which doubles as the drift
// heartbeat), and per-repo working trees. Multi-tenancy is a property of Git
// structure — one repo per customer/service.
package git

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
)

// ErrBadSignature indicates a missing or invalid webhook signature.
var ErrBadSignature = errors.New("git: webhook signature verification failed")

// VerifySignature checks a GitHub-style HMAC-SHA256 webhook signature against
// the raw request body. The header is "sha256=<hex>". A missing or invalid
// signature is rejected so a forged webhook never triggers reconciliation; the
// poll path remains the trusted fallback.
func VerifySignature(secret, body []byte, signatureHeader string) error {
	const prefix = "sha256="
	if !strings.HasPrefix(signatureHeader, prefix) {
		return ErrBadSignature
	}
	want, err := hex.DecodeString(strings.TrimPrefix(signatureHeader, prefix))
	if err != nil {
		return ErrBadSignature
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	got := mac.Sum(nil)
	if !hmac.Equal(got, want) {
		return ErrBadSignature
	}
	return nil
}

// Sign produces the signature header for a body — used in tests and for
// outbound webhooks if ever needed.
func Sign(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}
