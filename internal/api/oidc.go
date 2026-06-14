package api

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"math/big"
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
	HMACSecret string    // HS256 (shared-secret) verification
	Keys       KeySource // RS*/ES* verification against the IdP's published JWKS
	Now        func() time.Time
}

// OIDCAuth validates a compact JWT and maps claims to a Rollops identity. It
// verifies HS256 with a shared secret, or RS256/384/512 and ES256/384/512
// against an IdP's JWKS (set Config.Keys) — so a real OIDC provider's
// asymmetric, rotating keys work without a shared secret or external proxy.
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
	Kid string `json:"kid"`
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
	if err := verifySignature(h, parts[0]+"."+parts[1], parts[2], cfg); err != nil {
		return oidcClaims{}, err
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

// verifySignature checks the JWT signature per its alg: HS256 against the shared
// secret, RS*/ES* against the JWKS key for the token's kid.
func verifySignature(h oidcHeader, signingInput, sig string, cfg OIDCConfig) error {
	switch h.Alg {
	case "HS256":
		if !validHMAC(signingInput, sig, cfg.HMACSecret) {
			return errors.New("oidc: invalid HS256 signature")
		}
		return nil
	case "RS256", "RS384", "RS512", "ES256", "ES384", "ES512":
		if cfg.Keys == nil {
			return fmt.Errorf("oidc: alg %s requires a JWKS key source", h.Alg)
		}
		key, err := cfg.Keys.Key(h.Kid)
		if err != nil {
			return err
		}
		return verifyAsymmetric(h.Alg, signingInput, sig, key)
	default:
		return errors.New("oidc: unsupported alg " + h.Alg)
	}
}

func verifyAsymmetric(alg, signingInput, sigB64 string, key crypto.PublicKey) error {
	sig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		return fmt.Errorf("oidc: decode signature: %w", err)
	}
	h, cryptoHash := hashFor(alg)
	h.Write([]byte(signingInput))
	digest := h.Sum(nil)

	switch alg[:2] {
	case "RS":
		pub, ok := key.(*rsa.PublicKey)
		if !ok {
			return fmt.Errorf("oidc: %s token but key is %T", alg, key)
		}
		if err := rsa.VerifyPKCS1v15(pub, cryptoHash, digest, sig); err != nil {
			return fmt.Errorf("oidc: %s verification failed: %w", alg, err)
		}
		return nil
	case "ES":
		pub, ok := key.(*ecdsa.PublicKey)
		if !ok {
			return fmt.Errorf("oidc: %s token but key is %T", alg, key)
		}
		// JWS ECDSA signatures are fixed-length r||s, not ASN.1 DER.
		n := (pub.Curve.Params().BitSize + 7) / 8
		if len(sig) != 2*n {
			return fmt.Errorf("oidc: %s signature length %d, want %d", alg, len(sig), 2*n)
		}
		r := new(big.Int).SetBytes(sig[:n])
		s := new(big.Int).SetBytes(sig[n:])
		if !ecdsa.Verify(pub, digest, r, s) {
			return fmt.Errorf("oidc: %s verification failed", alg)
		}
		return nil
	default:
		return errors.New("oidc: unsupported alg " + alg)
	}
}

func hashFor(alg string) (hash.Hash, crypto.Hash) {
	switch alg[2:] {
	case "384":
		return sha512.New384(), crypto.SHA384
	case "512":
		return sha512.New(), crypto.SHA512
	default: // 256
		return sha256.New(), crypto.SHA256
	}
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
