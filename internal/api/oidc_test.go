package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/rollops/internal/security"
)

func TestOIDCAuth_MapsClaimsToRBACGroups(t *testing.T) {
	h := newServerWithAuth(t, ChainAuth{OIDCAuth{Config: OIDCConfig{
		Issuer:     "https://idp.example",
		Audience:   "rollops",
		HMACSecret: "secret",
		Now:        func() time.Time { return time.Unix(100, 0) },
	}}}, func(p *security.Policy) {
		p.DefineRole(security.Role{Name: "oidc-op", Grants: []security.Grant{{Perm: security.PermPlan}}})
		p.Bind("group:rollops-operators", "oidc-op")
	})
	token := signedJWT(t, "secret", map[string]any{
		"iss": "https://idp.example", "aud": "rollops", "exp": float64(200),
		"preferred_username": "ada", "groups": []string{"rollops-operators"},
	})
	if rr := do(h, "POST", "/v1/plan", token, cfgYAML); rr.Code != http.StatusOK {
		t.Fatalf("oidc plan = %d: %s", rr.Code, rr.Body)
	}
}

func TestOIDCAuth_RejectsInvalidToken(t *testing.T) {
	auth := OIDCAuth{Config: OIDCConfig{Issuer: "issuer", Audience: "rollops", HMACSecret: "secret", Now: func() time.Time {
		return time.Unix(100, 0)
	}}}
	tests := []struct {
		name  string
		token string
	}{
		{name: "bad signature", token: signedJWT(t, "wrong", map[string]any{"iss": "issuer", "aud": "rollops", "exp": float64(200), "sub": "u"})},
		{name: "bad issuer", token: signedJWT(t, "secret", map[string]any{"iss": "other", "aud": "rollops", "exp": float64(200), "sub": "u"})},
		{name: "expired", token: signedJWT(t, "secret", map[string]any{"iss": "issuer", "aud": "rollops", "exp": float64(50), "sub": "u"})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, ok := auth.Identify(tt.token); ok {
				t.Fatal("invalid token should not identify")
			}
		})
	}
}

func signedJWT(t *testing.T, secret string, claims map[string]any) string {
	t.Helper()
	header := map[string]any{"alg": "HS256", "typ": "JWT"}
	enc := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(b)
	}
	signed := enc(header) + "." + enc(claims)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signed))
	return strings.Join([]string{signed, base64.RawURLEncoding.EncodeToString(mac.Sum(nil))}, ".")
}
