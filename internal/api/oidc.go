package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"go.klarlabs.de/rollops/internal/rollout"
)

// ChainAuth tries authenticators in order. It lets deployments keep bootstrap
// tokens while also accepting an external IdP token.
type ChainAuth []Authenticator

func (c ChainAuth) Identify(token string) (rollout.Identity, bool) {
	for _, a := range c {
		if id, ok := a.Identify(token); ok {
			return id, true
		}
	}
	return rollout.Identity{}, false
}

type OIDCConfig struct {
	Issuer     string
	Audience   string
	HMACSecret string
	Now        func() time.Time
}

// OIDCAuth validates a compact HS256 JWT and maps claims to a Rollops identity.
// It is intentionally small and strict; production deployments can put a full
// OIDC proxy/JWKS verifier in front and still feed bearer identities through the
// same Authenticator boundary.
type OIDCAuth struct {
	Config OIDCConfig
}

func (a OIDCAuth) Identify(token string) (rollout.Identity, bool) {
	claims, err := a.claims(token)
	if err != nil {
		return rollout.Identity{}, false
	}
	name := firstNonEmpty(claims.PreferredUsername, claims.Email, claims.Sub)
	if name == "" {
		return rollout.Identity{}, false
	}
	return rollout.Identity{Kind: "human", Name: name, Groups: claims.Groups}, true
}

type oidcHeader struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
}

type oidcClaims struct {
	Issuer            string   `json:"iss"`
	Sub               string   `json:"sub"`
	Audience          any      `json:"aud"`
	Expiry            int64    `json:"exp"`
	Email             string   `json:"email"`
	PreferredUsername string   `json:"preferred_username"`
	Groups            []string `json:"groups"`
}

func (a OIDCAuth) claims(token string) (oidcClaims, error) {
	cfg := a.Config
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return oidcClaims{}, errors.New("oidc: malformed token")
	}
	var h oidcHeader
	if err := decodeJWT(parts[0], &h); err != nil {
		return oidcClaims{}, err
	}
	if h.Alg != "HS256" {
		return oidcClaims{}, errors.New("oidc: unsupported alg")
	}
	if !validHMAC(parts[0]+"."+parts[1], parts[2], cfg.HMACSecret) {
		return oidcClaims{}, errors.New("oidc: invalid signature")
	}
	var c oidcClaims
	if err := decodeJWT(parts[1], &c); err != nil {
		return oidcClaims{}, err
	}
	if c.Issuer != cfg.Issuer {
		return oidcClaims{}, errors.New("oidc: issuer mismatch")
	}
	if !audienceContains(c.Audience, cfg.Audience) {
		return oidcClaims{}, errors.New("oidc: audience mismatch")
	}
	if c.Expiry <= cfg.Now().Unix() {
		return oidcClaims{}, errors.New("oidc: token expired")
	}
	return c, nil
}

func decodeJWT(part string, out any) error {
	b, err := base64.RawURLEncoding.DecodeString(part)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func validHMAC(signed, sig, secret string) bool {
	if secret == "" {
		return false
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signed))
	return hmac.Equal(got, mac.Sum(nil))
}

func audienceContains(raw any, want string) bool {
	switch v := raw.(type) {
	case string:
		return v == want
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok && s == want {
				return true
			}
		}
	}
	return false
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
