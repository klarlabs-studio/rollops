package secrets

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// httpDoer is the slice of http.Client the Vault provider needs (injectable for
// tests).
type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// VaultProvider resolves secrets from HashiCorp Vault's KV v2 engine. The token
// is itself supplied by workload identity or a vault-managed bootstrap secret —
// never plaintext on disk. Refs are "mount/path#key", e.g. "kv/prod/db#password".
type VaultProvider struct {
	Addr   string // e.g. https://vault.internal:8200
	Token  string
	Mount  string // default KV mount when a ref omits one
	Client httpDoer
}

func (v VaultProvider) client() httpDoer {
	if v.Client != nil {
		return v.Client
	}
	return http.DefaultClient
}

// Resolve fetches mount/path#key from Vault KV v2.
func (v VaultProvider) Resolve(ctx context.Context, ref string) (Secret, error) {
	mount, path, key, err := parseRef(ref, v.Mount)
	if err != nil {
		return Secret{}, err
	}
	url := fmt.Sprintf("%s/v1/%s/data/%s", strings.TrimRight(v.Addr, "/"), mount, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Secret{}, err
	}
	req.Header.Set("X-Vault-Token", v.Token)

	resp, err := v.client().Do(req)
	if err != nil {
		return Secret{}, fmt.Errorf("secrets: vault: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return Secret{}, fmt.Errorf("%w: vault %s", ErrNotFound, ref)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return Secret{}, fmt.Errorf("secrets: vault status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out struct {
		Data struct {
			Data map[string]string `json:"data"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Secret{}, fmt.Errorf("secrets: vault decode: %w", err)
	}
	val, ok := out.Data.Data[key]
	if !ok {
		return Secret{}, fmt.Errorf("%w: key %q in %s", ErrNotFound, key, ref)
	}
	return NewSecret(val), nil
}

// parseRef splits "mount/path#key" (mount optional, falls back to defaultMount).
func parseRef(ref, defaultMount string) (mount, path, key string, err error) {
	hash := strings.LastIndex(ref, "#")
	if hash < 0 {
		return "", "", "", fmt.Errorf("secrets: ref %q must be mount/path#key", ref)
	}
	key = ref[hash+1:]
	loc := ref[:hash]
	if i := strings.Index(loc, "/"); i >= 0 && strings.Count(loc, "/") >= 1 {
		// Treat the first segment as the mount only if a default isn't forced.
		mount = loc[:i]
		path = loc[i+1:]
	} else {
		mount = defaultMount
		path = loc
	}
	if mount == "" || path == "" || key == "" {
		return "", "", "", fmt.Errorf("secrets: ref %q must be mount/path#key", ref)
	}
	return mount, path, key, nil
}
