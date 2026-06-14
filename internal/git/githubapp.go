package git

import (
	"bytes"
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
	"io"
	"net/http"
	"sync"
	"time"
)

// GitHubApp mints short-lived GitHub App installation access tokens for one
// installation, refreshing them on demand. This is the least-privilege,
// auto-rotating alternative to a long-lived PAT: an installation token is scoped
// to the repositories the App is installed on (which can span orgs and personal
// accounts via separate installations) and expires within an hour. The token is
// resolved per git command via Auth.TokenSource, so it is never pinned at
// startup and never written to disk.
type GitHubApp struct {
	appID          string
	installationID string
	key            *rsa.PrivateKey

	apiBase string
	http    *http.Client
	now     func() time.Time

	mu    sync.Mutex
	token string
	exp   time.Time
}

// GitHubAppOption configures a GitHubApp.
type GitHubAppOption func(*GitHubApp)

// WithGitHubAPIBase overrides the GitHub API base URL (for GitHub Enterprise or
// tests). Default https://api.github.com.
func WithGitHubAPIBase(base string) GitHubAppOption {
	return func(g *GitHubApp) { g.apiBase = base }
}

// WithHTTPClient sets the HTTP client used for token exchange.
func WithHTTPClient(c *http.Client) GitHubAppOption {
	return func(g *GitHubApp) { g.http = c }
}

// withClock injects a clock for deterministic tests.
func withClock(now func() time.Time) GitHubAppOption {
	return func(g *GitHubApp) { g.now = now }
}

// NewGitHubApp builds a provider from the App id, the target installation id,
// and the App's PEM-encoded RSA private key (PKCS#1 or PKCS#8).
func NewGitHubApp(appID, installationID string, pemKey []byte, opts ...GitHubAppOption) (*GitHubApp, error) {
	if appID == "" || installationID == "" {
		return nil, fmt.Errorf("githubapp: appID and installationID are required")
	}
	key, err := parseRSAPrivateKey(pemKey)
	if err != nil {
		return nil, err
	}
	g := &GitHubApp{
		appID:          appID,
		installationID: installationID,
		key:            key,
		apiBase:        "https://api.github.com",
		http:           http.DefaultClient,
		now:            time.Now,
	}
	for _, o := range opts {
		o(g)
	}
	return g, nil
}

// Token returns a valid installation token, minting a new one when the cached
// token is absent or within 60s of expiry. Safe for concurrent use. Its
// signature matches TokenProvider, so &app.Token can be set as Auth.TokenSource.
func (g *GitHubApp) Token(ctx context.Context) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.token != "" && g.now().Before(g.exp.Add(-60*time.Second)) {
		return g.token, nil
	}
	tok, exp, err := g.mint(ctx)
	if err != nil {
		return "", err
	}
	g.token, g.exp = tok, exp
	return tok, nil
}

// mint exchanges a freshly signed App JWT for an installation access token.
func (g *GitHubApp) mint(ctx context.Context) (string, time.Time, error) {
	jwt, err := g.signJWT()
	if err != nil {
		return "", time.Time{}, err
	}
	url := fmt.Sprintf("%s/app/installations/%s/access_tokens", g.apiBase, g.installationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := g.http.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("githubapp: installation token request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusCreated {
		// The body may include an error message but never the App key or a token.
		return "", time.Time{}, fmt.Errorf("githubapp: installation token status %d: %s", resp.StatusCode, bytes.TrimSpace(body))
	}
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", time.Time{}, fmt.Errorf("githubapp: parse installation token: %w", err)
	}
	if out.Token == "" {
		return "", time.Time{}, fmt.Errorf("githubapp: empty installation token in response")
	}
	if out.ExpiresAt.IsZero() {
		out.ExpiresAt = g.now().Add(time.Hour)
	}
	return out.Token, out.ExpiresAt, nil
}

// signJWT builds and RS256-signs a GitHub App JWT (iss=appID, ~9m lifetime,
// iat backdated 60s to tolerate clock skew).
func (g *GitHubApp) signJWT() (string, error) {
	now := g.now()
	header := base64URL([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, err := json.Marshal(map[string]any{
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": g.appID,
	})
	if err != nil {
		return "", err
	}
	signingInput := header + "." + base64URL(claims)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, g.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("githubapp: sign jwt: %w", err)
	}
	return signingInput + "." + base64URL(sig), nil
}

func base64URL(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// parseRSAPrivateKey accepts a PEM RSA key in PKCS#1 or PKCS#8 form.
func parseRSAPrivateKey(pemKey []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemKey)
	if block == nil {
		return nil, fmt.Errorf("githubapp: no PEM block in private key")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	keyAny, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("githubapp: parse private key (PKCS#1/#8): %w", err)
	}
	key, ok := keyAny.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("githubapp: private key is %T, want RSA", keyAny)
	}
	return key, nil
}
