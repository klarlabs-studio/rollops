package api

import (
	"sync"
	"testing"

	"go.klarlabs.de/rollops/internal/rollout"
)

func TestSwappableTokenAuth_IdentifiesAndRotates(t *testing.T) {
	auth := NewSwappableTokenAuth(TokenAuth{"tok-old": {Kind: "agent", Name: "nomi"}})

	id, ok := auth.Identify("tok-old")
	if !ok || id.Name != "nomi" {
		t.Fatalf("Identify(tok-old) = %+v, %v; want nomi", id, ok)
	}

	// Rotate: the old token stops working, the new one starts, with no restart.
	auth.Replace(TokenAuth{"tok-new": {Kind: "agent", Name: "nomi"}})
	if _, ok := auth.Identify("tok-old"); ok {
		t.Error("the rotated-out token must stop authenticating")
	}
	id, ok = auth.Identify("tok-new")
	if !ok || id.Name != "nomi" {
		t.Errorf("Identify(tok-new) = %+v, %v; want nomi", id, ok)
	}
}

// TestSwappableTokenAuth_FailsClosed pins the fail-closed direction: no tokens
// means nothing authenticates, rather than everything.
func TestSwappableTokenAuth_FailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name string
		auth *SwappableTokenAuth
	}{
		{"empty map", NewSwappableTokenAuth(TokenAuth{})},
		{"nil map", NewSwappableTokenAuth(nil)},
		{"zero value", &SwappableTokenAuth{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := tc.auth.Identify("anything"); ok {
				t.Error("no configured tokens must reject every caller")
			}
			if _, ok := tc.auth.Identify(""); ok {
				t.Error("an empty token must never authenticate")
			}
			if n := tc.auth.Len(); n != 0 {
				t.Errorf("Len = %d, want 0", n)
			}
		})
	}
}

// TestSwappableTokenAuth_ConcurrentReadsDuringRotation is the race the atomic
// swap exists for: callers authenticating while an operator rotates tokens.
// Run with -race.
func TestSwappableTokenAuth_ConcurrentReadsDuringRotation(t *testing.T) {
	auth := NewSwappableTokenAuth(TokenAuth{"tok-a": {Kind: "agent", Name: "nomi"}})
	var wg sync.WaitGroup
	stop := make(chan struct{})

	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					// Every observed state must be internally consistent: a token
					// either resolves to a real identity or not at all — never to a
					// half-applied map.
					if id, ok := auth.Identify("tok-a"); ok && id.Name == "" {
						t.Error("observed a torn identity mid-rotation")
						return
					}
				}
			}
		}()
	}
	for i := range 200 {
		if i%2 == 0 {
			auth.Replace(TokenAuth{"tok-a": {Kind: "agent", Name: "nomi"}})
		} else {
			auth.Replace(TokenAuth{"tok-b": {Kind: "agent", Name: "deploy-bot"}})
		}
	}
	close(stop)
	wg.Wait()
}

// TestSwappableTokenAuth_SatisfiesAuthenticator keeps it usable everywhere the
// daemon expects an Authenticator (the MCP serve options take the interface).
func TestSwappableTokenAuth_SatisfiesAuthenticator(t *testing.T) {
	var _ Authenticator = NewSwappableTokenAuth(TokenAuth{})
	var _ Authenticator = (*SwappableTokenAuth)(nil)
	_ = rollout.Identity{}
}
