package git

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testKeyPEM(t *testing.T) ([]byte, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return pemBytes, key
}

// tokenServer returns a fake GitHub installation-token endpoint that verifies
// the App JWT and counts how many tokens it has minted.
func tokenServer(t *testing.T, pub *rsa.PublicKey, calls *int, expiry func() time.Time) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/access_tokens") || r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if err := verifyJWT(bearer, pub); err != nil {
			t.Errorf("invalid App JWT: %v", err)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		*calls++
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"token":"ghs_tok%d","expires_at":%q}`, *calls, expiry().UTC().Format(time.RFC3339))
	}))
}

func verifyJWT(token string, pub *rsa.PublicKey) error {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return fmt.Errorf("want 3 JWT parts, got %d", len(parts))
	}
	var claims struct {
		Iss string `json:"iss"`
		Exp int64  `json:"exp"`
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return err
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return err
	}
	if claims.Iss != "app-123" {
		return fmt.Errorf("iss = %q, want app-123", claims.Iss)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	return rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig)
}

func TestGitHubApp_MintsAndCachesAndRefreshes(t *testing.T) {
	pemBytes, key := testKeyPEM(t)
	now := time.Unix(1_700_000_000, 0)
	calls := 0
	// Tokens expire 1h after the (frozen) current time.
	srv := tokenServer(t, &key.PublicKey, &calls, func() time.Time { return now.Add(time.Hour) })
	defer srv.Close()

	app, err := NewGitHubApp("app-123", "456", pemBytes,
		WithGitHubAPIBase(srv.URL), WithHTTPClient(srv.Client()), withClock(func() time.Time { return now }))
	if err != nil {
		t.Fatalf("NewGitHubApp: %v", err)
	}
	ctx := context.Background()

	tok, err := app.Token(ctx)
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok != "ghs_tok1" || calls != 1 {
		t.Fatalf("first token = %q calls=%d, want ghs_tok1/1", tok, calls)
	}
	// Cached: no new mint while well within expiry.
	if tok2, _ := app.Token(ctx); tok2 != "ghs_tok1" || calls != 1 {
		t.Fatalf("second token = %q calls=%d, want cached ghs_tok1/1", tok2, calls)
	}
	// Advance past expiry (minus the 60s safety margin) → refresh.
	now = now.Add(time.Hour)
	if tok3, _ := app.Token(ctx); tok3 != "ghs_tok2" || calls != 2 {
		t.Fatalf("refreshed token = %q calls=%d, want ghs_tok2/2", tok3, calls)
	}
}

func TestGitHubApp_TokenSourceFulfilsProvider(t *testing.T) {
	pemBytes, key := testKeyPEM(t)
	now := time.Unix(1_700_000_000, 0)
	calls := 0
	srv := tokenServer(t, &key.PublicKey, &calls, func() time.Time { return now.Add(time.Hour) })
	defer srv.Close()
	app, _ := NewGitHubApp("app-123", "456", pemBytes,
		WithGitHubAPIBase(srv.URL), WithHTTPClient(srv.Client()), withClock(func() time.Time { return now }))

	// app.Token must satisfy the Auth.TokenSource seam and resolve via Auth.token.
	auth := Auth{TokenSource: app.Token}
	got, err := auth.token(context.Background())
	if err != nil || got != "ghs_tok1" {
		t.Fatalf("auth.token via provider = %q err=%v", got, err)
	}
}

func TestGitHubApp_ErrorOnBadStatus(t *testing.T) {
	pemBytes, _ := testKeyPEM(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprint(w, `{"message":"Bad credentials"}`)
	}))
	defer srv.Close()
	app, _ := NewGitHubApp("app-123", "456", pemBytes, WithGitHubAPIBase(srv.URL), WithHTTPClient(srv.Client()))
	if _, err := app.Token(context.Background()); err == nil || !strings.Contains(err.Error(), "status 403") {
		t.Fatalf("err = %v, want status 403", err)
	}
}

func TestGitHubApp_RequiresFields(t *testing.T) {
	pemBytes, _ := testKeyPEM(t)
	if _, err := NewGitHubApp("", "456", pemBytes); err == nil {
		t.Error("missing appID must error")
	}
	if _, err := NewGitHubApp("app-123", "456", []byte("not a pem")); err == nil {
		t.Error("bad PEM must error")
	}
}

func TestParseRSAPrivateKey_PKCS8(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8 := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if _, err := parseRSAPrivateKey(pkcs8); err != nil {
		t.Fatalf("PKCS#8 key must parse: %v", err)
	}
}
