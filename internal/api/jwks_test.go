package api

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func b64u(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// signRS256 builds a compact RS256 JWT with the given kid.
func signRS256(t *testing.T, key *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	hdr, _ := json.Marshal(map[string]any{"alg": "RS256", "typ": "JWT", "kid": kid})
	pl, _ := json.Marshal(claims)
	signing := b64u(hdr) + "." + b64u(pl)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return signing + "." + b64u(sig)
}

func signES256(t *testing.T, key *ecdsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	hdr, _ := json.Marshal(map[string]any{"alg": "ES256", "typ": "JWT", "kid": kid})
	pl, _ := json.Marshal(claims)
	signing := b64u(hdr) + "." + b64u(pl)
	sum := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, key, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	// JWS wants fixed-length r||s.
	n := (key.Curve.Params().BitSize + 7) / 8
	sig := append(leftPad(r.Bytes(), n), leftPad(s.Bytes(), n)...)
	return signing + "." + b64u(sig)
}

func leftPad(b []byte, n int) []byte {
	if len(b) >= n {
		return b
	}
	out := make([]byte, n)
	copy(out[n-len(b):], b)
	return out
}

// ecCoords returns the fixed-length big-endian X and Y coordinates of an
// EC public key via its uncompressed point encoding (0x04 || X || Y).
func ecCoords(t *testing.T, pub *ecdsa.PublicKey) ([]byte, []byte) {
	t.Helper()
	ek, err := pub.ECDH()
	if err != nil {
		t.Fatal(err)
	}
	b := ek.Bytes() // 0x04 || X || Y
	n := (len(b) - 1) / 2
	return b[1 : 1+n], b[1+n:]
}

func ecX(t *testing.T, pub *ecdsa.PublicKey) []byte { x, _ := ecCoords(t, pub); return x }
func ecY(t *testing.T, pub *ecdsa.PublicKey) []byte { _, y := ecCoords(t, pub); return y }

// jwksServer serves a JWKS document with one RSA and one EC key.
func jwksServer(t *testing.T, rsaKey *rsa.PublicKey, ecKey *ecdsa.PublicKey, calls *int) *httptest.Server {
	t.Helper()
	keys := []map[string]any{
		{"kid": "rsa1", "kty": "RSA", "n": b64u(rsaKey.N.Bytes()), "e": b64u(big.NewInt(int64(rsaKey.E)).Bytes())},
		{"kid": "ec1", "kty": "EC", "crv": "P-256", "x": b64u(ecX(t, ecKey)), "y": b64u(ecY(t, ecKey))},
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls != nil {
			*calls++
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": keys})
	}))
}

func oidcClaimsMap() map[string]any {
	return map[string]any{"iss": "https://idp.example", "aud": "rollops", "exp": float64(200), "preferred_username": "ada"}
}

func TestOIDC_RS256_ViaJWKS(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	calls := 0
	srv := jwksServer(t, &key.PublicKey, &ec.PublicKey, &calls)
	defer srv.Close()

	jwks := NewJWKS(srv.URL, WithJWKSHTTPClient(srv.Client()), withJWKSClock(func() time.Time { return time.Unix(100, 0) }))
	auth := OIDCAuth{Config: OIDCConfig{Issuer: "https://idp.example", Audience: "rollops", Keys: jwks, Now: func() time.Time { return time.Unix(100, 0) }}}

	id, ok := auth.Identify(signRS256(t, key, "rsa1", oidcClaimsMap()))
	if !ok || id.Name != "ada" {
		t.Fatalf("RS256 via JWKS should authenticate, got ok=%v id=%+v", ok, id)
	}
	// Second call is cached (no refetch).
	auth.Identify(signRS256(t, key, "rsa1", oidcClaimsMap()))
	if calls != 1 {
		t.Errorf("JWKS should be cached, fetched %d times", calls)
	}
}

func TestOIDC_ES256_ViaJWKS(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	srv := jwksServer(t, &key.PublicKey, &ec.PublicKey, nil)
	defer srv.Close()
	jwks := NewJWKS(srv.URL, WithJWKSHTTPClient(srv.Client()))
	auth := OIDCAuth{Config: OIDCConfig{Issuer: "https://idp.example", Audience: "rollops", Keys: jwks, Now: func() time.Time { return time.Unix(100, 0) }}}

	if id, ok := auth.Identify(signES256(t, ec, "ec1", oidcClaimsMap())); !ok || id.Name != "ada" {
		t.Fatalf("ES256 via JWKS should authenticate, got ok=%v id=%+v", ok, id)
	}
}

func TestOIDC_RejectsWrongKeyAndUnknownKid(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	srv := jwksServer(t, &key.PublicKey, &ec.PublicKey, nil)
	defer srv.Close()
	jwks := NewJWKS(srv.URL, WithJWKSHTTPClient(srv.Client()), withJWKSClock(func() time.Time { return time.Unix(100, 0) }))
	auth := OIDCAuth{Config: OIDCConfig{Issuer: "https://idp.example", Audience: "rollops", Keys: jwks, Now: func() time.Time { return time.Unix(100, 0) }}}

	// Signed by a different key but claims kid rsa1 → signature mismatch.
	if _, ok := auth.Identify(signRS256(t, other, "rsa1", oidcClaimsMap())); ok {
		t.Error("token signed by wrong key must be rejected")
	}
	// Unknown kid → no key.
	if _, ok := auth.Identify(signRS256(t, key, "nope", oidcClaimsMap())); ok {
		t.Error("unknown kid must be rejected")
	}
}

func TestOIDC_RS256_RequiresKeySource(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	// No Keys configured and only HS256 secret → RS256 token rejected.
	auth := OIDCAuth{Config: OIDCConfig{Issuer: "https://idp.example", Audience: "rollops", HMACSecret: "s", Now: func() time.Time { return time.Unix(100, 0) }}}
	if _, ok := auth.Identify(signRS256(t, key, "rsa1", oidcClaimsMap())); ok {
		t.Error("RS256 without a JWKS key source must be rejected")
	}
}

// TestJWKS_UnknownKidDoesNotRefetch proves the negative-cache / rate-limit: once
// the key set is fetched, a flood of unknown kids does not trigger a network
// fetch per lookup (the JWKS-refresh DoS).
func TestJWKS_UnknownKidDoesNotRefetch(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	calls := 0
	srv := jwksServer(t, &key.PublicKey, &ec.PublicKey, &calls)
	defer srv.Close()

	jwks := NewJWKS(srv.URL, WithJWKSHTTPClient(srv.Client()), withJWKSClock(func() time.Time { return time.Unix(100, 0) }))

	// Prime the cache with a real fetch.
	if _, err := jwks.Key("rsa1"); err != nil {
		t.Fatalf("priming fetch: %v", err)
	}
	if calls != 1 {
		t.Fatalf("priming fetched %d times, want 1", calls)
	}
	// A burst of unknown (random) kids must not trigger further fetches while the
	// cached set is still valid.
	for i := 0; i < 50; i++ {
		if _, err := jwks.Key(fmt.Sprintf("attacker-kid-%d", i)); err == nil {
			t.Fatalf("unknown kid %d should not resolve", i)
		}
	}
	if calls != 1 {
		t.Errorf("unknown-kid flood forced %d fetches, want 1 (negative cache broken)", calls)
	}
}

// TestJWKS_FetchIsTimeBounded proves a slow/hung IdP cannot stall the caller: the
// fetch is bounded by the HTTP client timeout, so Key returns an error promptly.
func TestJWKS_FetchIsTimeBounded(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release // hang until the test ends
	}))
	// Defers run LIFO: unblock the handler (close release) BEFORE srv.Close, which
	// blocks until in-flight handlers return.
	defer srv.Close()
	defer close(release)

	client := srv.Client()
	client.Timeout = 100 * time.Millisecond
	jwks := NewJWKS(srv.URL, WithJWKSHTTPClient(client))

	done := make(chan error, 1)
	start := time.Now()
	go func() { _, err := jwks.Key("any"); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected a timeout error from a hung JWKS endpoint")
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Errorf("fetch took %s, want time-bounded (~timeout)", elapsed)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Key did not return; fetch is not time-bounded (mutex held across network call?)")
	}
}

// TestJWKS_ConcurrentUnknownKidSingleFetch proves a burst of concurrent lookups
// is deduplicated into a single in-flight fetch rather than N serialized ones.
func TestJWKS_ConcurrentUnknownKidSingleFetch(t *testing.T) {
	var calls int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&calls, 1)
		time.Sleep(150 * time.Millisecond) // widen the race window
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{
			{"kid": "rsa1", "kty": "RSA", "n": "AQAB", "e": "AQAB"},
		}})
	}))
	defer srv.Close()

	jwks := NewJWKS(srv.URL, WithJWKSHTTPClient(srv.Client()))
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = jwks.Key("nope") }()
	}
	wg.Wait()
	if n := atomic.LoadInt64(&calls); n != 1 {
		t.Errorf("concurrent unknown-kid lookups fetched %d times, want 1 (no singleflight)", n)
	}
}

func TestDiscoverJWKSURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/openid-configuration" {
			_ = json.NewEncoder(w).Encode(map[string]any{"jwks_uri": "https://idp.example/keys"})
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()
	got, err := DiscoverJWKSURL(srv.Client(), srv.URL)
	if err != nil || got != "https://idp.example/keys" {
		t.Fatalf("discovery = %q err=%v", got, err)
	}
}
