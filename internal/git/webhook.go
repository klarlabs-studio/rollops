// Package git is the Git integration: per-repo working trees, poll as the
// reconcile trigger (which doubles as the drift heartbeat), and HMAC-SHA256
// GitHub webhooks that tick matching watched repos. Multi-tenancy is a
// property of Git structure — one repo per customer/service.
package git

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

// ErrBadSignature indicates a missing or invalid webhook signature.
var ErrBadSignature = errors.New("git: webhook signature verification failed")

// SignatureHeader is the GitHub HMAC-SHA256 header name.
const SignatureHeader = "X-Hub-Signature-256"

// maxWebhookBytes caps an inbound GitHub payload so a oversized body cannot
// exhaust memory. GitHub push events are well under this.
const maxWebhookBytes = 1 << 20 // 1 MiB

// VerifySignature checks a GitHub-style HMAC-SHA256 webhook signature against
// the raw request body. The header is "sha256=<hex>". A missing or invalid
// signature is rejected so a forged payload never triggers reconciliation.
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

// GitHubRepoHint extracts a repo identity from a GitHub webhook JSON body.
// Prefer repository.full_name ("owner/repo"); fall back to clone_url. An
// unparseable or ping-without-repo payload returns "" so the watcher ticks
// every watched repo (still bounded by the watch list).
func GitHubRepoHint(body []byte) string {
	var p struct {
		Repository struct {
			FullName string `json:"full_name"`
			CloneURL string `json:"clone_url"`
			HTMLURL  string `json:"html_url"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return ""
	}
	switch {
	case p.Repository.FullName != "":
		return p.Repository.FullName
	case p.Repository.CloneURL != "":
		return p.Repository.CloneURL
	default:
		return p.Repository.HTMLURL
	}
}

// SameRepo reports whether a watched remote URL and a webhook hint name the
// same GitHub repository (https, ssh, and owner/repo forms).
func SameRepo(watchURL, hint string) bool {
	a, aok := repoKey(watchURL)
	b, bok := repoKey(hint)
	return aok && bok && a == b
}

func repoKey(s string) (string, bool) {
	owner, repo, err := ownerRepoFromURL(s)
	if err != nil {
		return "", false
	}
	return strings.ToLower(owner + "/" + repo), true
}

// TickFunc is invoked after a webhook payload verifies. hint is a repo
// identity from the payload, or empty when unknown.
type TickFunc func(ctx context.Context, repoHint string)

// WebhookHandler serves POST /v1/hooks/github. HMAC is the only auth:
//
//   - secret unset → 404 (the route is not an unauthenticated tick)
//   - invalid or missing signature → 401, Tick is not called
//   - valid signature → Tick(hint), 202
func WebhookHandler(secret string, tick TickFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if secret == "" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBytes+1))
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if len(body) > maxWebhookBytes {
			http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
			return
		}
		if err := VerifySignature([]byte(secret), body, r.Header.Get(SignatureHeader)); err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		hint := GitHubRepoHint(body)
		if tick != nil {
			tick(r.Context(), hint)
		}
		w.WriteHeader(http.StatusAccepted)
	})
}
