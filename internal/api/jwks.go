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

// defaultJWKSTimeout bounds a single JWKS fetch so an unreachable or slow IdP
// can't wedge an inbound request indefinitely (the fetch happens before
// signature verification, so it is reachable pre-auth).
const defaultJWKSTimeout = 10 * time.Second

// defaultJWKSRefreshInterval rate-limits rotation-driven refetches. While the
// cached key set is still valid, an unknown `kid` triggers at most one refetch
// per interval, so a flood of unauthenticated tokens carrying random kids cannot
// force a network fetch per request (a JWKS-refresh DoS).
const defaultJWKSRefreshInterval = time.Minute

// JWKS fetches and caches an OIDC provider's JSON Web Key Set, refreshing on a
// TTL or whenever an unknown `kid` is seen (key rotation). It supports RSA and
// EC keys with stdlib only — no JWT/JWKS dependency, keeping the single static
// binary.
type JWKS struct {
	url        string
	http       *http.Client
	now        func() time.Time
	ttl        time.Duration
	minRefresh time.Duration // floor between rotation-driven refetches

	mu          sync.Mutex
	keys        map[string]crypto.PublicKey
	exp         time.Time
	lastRefresh time.Time     // when the last fetch attempt completed
	refreshing  bool          // a fetch is in flight (dedupes concurrent refresh)
	done        chan struct{} // closed when the in-flight fetch completes
}

// JWKSOption configures a JWKS.
type JWKSOption func(*JWKS)

// WithJWKSHTTPClient sets the HTTP client used to fetch the key set.
func WithJWKSHTTPClient(c *http.Client) JWKSOption { return func(j *JWKS) { j.http = c } }

func withJWKSClock(now func() time.Time) JWKSOption { return func(j *JWKS) { j.now = now } }

// NewJWKS builds a key source backed by the JWKS endpoint at url. The default
// HTTP client carries a timeout so a hung IdP can't stall request handling; pass
// WithJWKSHTTPClient to override it.
func NewJWKS(url string, opts ...JWKSOption) *JWKS {
	j := &JWKS{
		url:        url,
		http:       &http.Client{Timeout: defaultJWKSTimeout},
		now:        time.Now,
		ttl:        time.Hour,
		minRefresh: defaultJWKSRefreshInterval,
		keys:       map[string]crypto.PublicKey{},
	}
	for _, o := range opts {
		o(j)
	}
	// A caller-supplied client with no timeout would reintroduce the pre-auth
	// stall; give it a sane default rather than trusting the caller.
	if j.http == nil {
		j.http = &http.Client{Timeout: defaultJWKSTimeout}
	} else if j.http.Timeout == 0 {
		j.http.Timeout = defaultJWKSTimeout
	}
	return j
}

// Key returns the public key for kid, refreshing the cache when it is empty,
// expired, or missing the requested kid (rotation). The network fetch runs
// without holding the mutex, and is both rate-limited (minRefresh) and
// deduplicated (a single in-flight fetch) so a burst of tokens with unknown kids
// cannot force one serialized, un-throttled fetch per request.
func (j *JWKS) Key(kid string) (crypto.PublicKey, error) {
	j.mu.Lock()
	for {
		if k, ok := j.keys[kid]; ok && j.now().Before(j.exp) {
			j.mu.Unlock()
			return k, nil
		}
		// Cache still valid but missing this kid (possible rotation). Only refetch
		// once per minRefresh so random/unknown kids can't force a fetch each time.
		if j.now().Before(j.exp) && j.now().Before(j.lastRefresh.Add(j.minRefresh)) {
			j.mu.Unlock()
			return nil, fmt.Errorf("oidc: no JWKS key for kid %q", kid)
		}
		if j.refreshing {
			// A fetch is already running: wait for it and re-evaluate rather than
			// launching a second concurrent fetch.
			done := j.done
			j.mu.Unlock()
			<-done
			j.mu.Lock()
			continue
		}
		break
	}
	j.refreshing = true
	j.done = make(chan struct{})
	client, url := j.http, j.url
	j.mu.Unlock()

	keys, ferr := fetchJWKS(client, url)
	j.mu.Lock()
	j.lastRefresh = j.now()
	if ferr == nil {
		j.keys = keys
		j.exp = j.now().Add(j.ttl)
	}
	j.refreshing = false
	close(j.done)
	k, ok := j.keys[kid]
	j.mu.Unlock()
	if ok {
		return k, nil
	}
	if ferr != nil {
		return nil, ferr
	}
	return nil, fmt.Errorf("oidc: no JWKS key for kid %q", kid)
}

func fetchJWKS(client *http.Client, url string) (map[string]crypto.PublicKey, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("oidc: fetch JWKS: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("oidc: JWKS status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("oidc: read JWKS: %w", err)
	}
	return parseJWKS(body)
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
		return ecPublicKey(curve, k.X, k.Y)
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

// ecPublicKey builds an ECDSA public key from a JWK's base64url coordinates.
//
// It goes through ParseUncompressedPublicKey rather than assigning X and Y
// directly. Assigning them accepts any pair of integers, including a point
// that is not on the named curve, and this function parses keys fetched from
// an issuer's JWKS — precisely where such a point would arrive from.
// ParseUncompressedPublicKey checks curve membership; the deprecation of the
// raw coordinate fields in Go 1.26 says the same thing.
func ecPublicKey(curve elliptic.Curve, xs, ys string) (*ecdsa.PublicKey, error) {
	size := (curve.Params().BitSize + 7) / 8
	x, err := b64uCoord(xs, size)
	if err != nil {
		return nil, fmt.Errorf("oidc: jwk EC x: %w", err)
	}
	y, err := b64uCoord(ys, size)
	if err != nil {
		return nil, fmt.Errorf("oidc: jwk EC y: %w", err)
	}
	// Uncompressed point: 0x04 || X || Y, each coordinate left-padded to the
	// curve's byte length.
	buf := make([]byte, 1+2*size)
	buf[0] = 4
	copy(buf[1:1+size], x)
	copy(buf[1+size:], y)
	pub, err := ecdsa.ParseUncompressedPublicKey(curve, buf)
	if err != nil {
		return nil, fmt.Errorf("oidc: jwk EC point: %w", err)
	}
	return pub, nil
}

// b64uCoord decodes one base64url coordinate and left-pads it to the curve's
// byte length. RFC 7518 requires the encoding to already be that length, but a
// shorter one is unambiguous and some issuers strip leading zero bytes.
func b64uCoord(s string, size int) ([]byte, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	if len(b) > size {
		return nil, fmt.Errorf("coordinate is %d bytes, want at most %d", len(b), size)
	}
	out := make([]byte, size)
	copy(out[size-len(b):], b)
	return out, nil
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
	defer func() { _ = resp.Body.Close() }()
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
