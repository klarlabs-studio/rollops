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
	"math/big"
	"net/http"
	"net/http/httptest"
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

// jwksServer serves a JWKS document with one RSA and one EC key.
func jwksServer(t *testing.T, rsaKey *rsa.PublicKey, ecKey *ecdsa.PublicKey, calls *int) *httptest.Server {
	t.Helper()
	keys := []map[string]any{
		{"kid": "rsa1", "kty": "RSA", "n": b64u(rsaKey.N.Bytes()), "e": b64u(big.NewInt(int64(rsaKey.E)).Bytes())},
		{"kid": "ec1", "kty": "EC", "crv": "P-256", "x": b64u(ecKey.X.Bytes()), "y": b64u(ecKey.Y.Bytes())},
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls != nil {
			*calls++
		}
		json.NewEncoder(w).Encode(map[string]any{"keys": keys})
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

func TestDiscoverJWKSURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/openid-configuration" {
			json.NewEncoder(w).Encode(map[string]any{"jwks_uri": "https://idp.example/keys"})
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
