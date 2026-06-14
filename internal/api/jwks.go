package api

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sync"
	"time"
)

// KeySource resolves a JWT signing key by its `kid` header. JWKS implements it
// against an IdP's published key set; tests inject a static one.
type KeySource interface {
	Key(kid string) (crypto.PublicKey, error)
}

// JWKS fetches and caches an OIDC provider's JSON Web Key Set, refreshing on a
// TTL or whenever an unknown `kid` is seen (key rotation). It supports RSA and
// EC keys with stdlib only — no JWT/JWKS dependency, keeping the single static
// binary.
type JWKS struct {
	url  string
	http *http.Client
	now  func() time.Time
	ttl  time.Duration

	mu   sync.Mutex
	keys map[string]crypto.PublicKey
	exp  time.Time
}

// JWKSOption configures a JWKS.
type JWKSOption func(*JWKS)

// WithJWKSHTTPClient sets the HTTP client used to fetch the key set.
func WithJWKSHTTPClient(c *http.Client) JWKSOption { return func(j *JWKS) { j.http = c } }

func withJWKSClock(now func() time.Time) JWKSOption { return func(j *JWKS) { j.now = now } }

// NewJWKS builds a key source backed by the JWKS endpoint at url.
func NewJWKS(url string, opts ...JWKSOption) *JWKS {
	j := &JWKS{url: url, http: http.DefaultClient, now: time.Now, ttl: time.Hour, keys: map[string]crypto.PublicKey{}}
	for _, o := range opts {
		o(j)
	}
	return j
}

// Key returns the public key for kid, refreshing the cache when it is empty,
// expired, or missing the requested kid (rotation).
func (j *JWKS) Key(kid string) (crypto.PublicKey, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if k, ok := j.keys[kid]; ok && j.now().Before(j.exp) {
		return k, nil
	}
	if err := j.refresh(); err != nil {
		return nil, err
	}
	k, ok := j.keys[kid]
	if !ok {
		return nil, fmt.Errorf("oidc: no JWKS key for kid %q", kid)
	}
	return k, nil
}

func (j *JWKS) refresh() error {
	resp, err := j.http.Get(j.url)
	if err != nil {
		return fmt.Errorf("oidc: fetch JWKS: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("oidc: JWKS status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("oidc: read JWKS: %w", err)
	}
	keys, err := parseJWKS(body)
	if err != nil {
		return err
	}
	j.keys = keys
	j.exp = j.now().Add(j.ttl)
	return nil
}

type jwk struct {
	Kid string `json:"kid"`
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	N   string `json:"n"`
	E   string `json:"e"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

func parseJWKS(body []byte) (map[string]crypto.PublicKey, error) {
	var set struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.Unmarshal(body, &set); err != nil {
		return nil, fmt.Errorf("oidc: parse JWKS: %w", err)
	}
	out := make(map[string]crypto.PublicKey, len(set.Keys))
	for _, k := range set.Keys {
		pub, err := k.publicKey()
		if err != nil {
			return nil, err
		}
		out[k.Kid] = pub
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("oidc: empty JWKS")
	}
	return out, nil
}

func (k jwk) publicKey() (crypto.PublicKey, error) {
	switch k.Kty {
	case "RSA":
		n, err := b64uBigInt(k.N)
		if err != nil {
			return nil, fmt.Errorf("oidc: jwk RSA n: %w", err)
		}
		eb, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil {
			return nil, fmt.Errorf("oidc: jwk RSA e: %w", err)
		}
		e := 0
		for _, b := range eb {
			e = e<<8 | int(b)
		}
		if e == 0 {
			return nil, fmt.Errorf("oidc: jwk RSA exponent is zero")
		}
		return &rsa.PublicKey{N: n, E: e}, nil
	case "EC":
		curve, err := curveFor(k.Crv)
		if err != nil {
			return nil, err
		}
		x, err := b64uBigInt(k.X)
		if err != nil {
			return nil, fmt.Errorf("oidc: jwk EC x: %w", err)
		}
		y, err := b64uBigInt(k.Y)
		if err != nil {
			return nil, fmt.Errorf("oidc: jwk EC y: %w", err)
		}
		return &ecdsa.PublicKey{Curve: curve, X: x, Y: y}, nil
	default:
		return nil, fmt.Errorf("oidc: unsupported jwk kty %q", k.Kty)
	}
}

func curveFor(crv string) (elliptic.Curve, error) {
	switch crv {
	case "P-256":
		return elliptic.P256(), nil
	case "P-384":
		return elliptic.P384(), nil
	case "P-521":
		return elliptic.P521(), nil
	default:
		return nil, fmt.Errorf("oidc: unsupported EC curve %q", crv)
	}
}

func b64uBigInt(s string) (*big.Int, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	return new(big.Int).SetBytes(b), nil
}

// DiscoverJWKSURL resolves the jwks_uri from an issuer's OIDC discovery document
// (issuer + /.well-known/openid-configuration).
func DiscoverJWKSURL(httpc *http.Client, issuer string) (string, error) {
	if httpc == nil {
		httpc = http.DefaultClient
	}
	url := issuer + "/.well-known/openid-configuration"
	resp, err := httpc.Get(url)
	if err != nil {
		return "", fmt.Errorf("oidc: discovery: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("oidc: discovery status %d", resp.StatusCode)
	}
	var doc struct {
		JWKSURI string `json:"jwks_uri"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&doc); err != nil {
		return "", fmt.Errorf("oidc: parse discovery: %w", err)
	}
	if doc.JWKSURI == "" {
		return "", fmt.Errorf("oidc: discovery document has no jwks_uri")
	}
	return doc.JWKSURI, nil
}
