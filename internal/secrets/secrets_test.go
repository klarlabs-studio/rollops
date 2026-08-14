package secrets

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecret_Redacts(t *testing.T) {
	s := NewSecret("hunter2")
	if got := s.String(); got != "***" {
		t.Errorf("String() = %q, want ***", got)
	}
	if got := fmt.Sprintf("%v password=%s", "x", s); strings.Contains(got, "hunter2") {
		t.Errorf("secret leaked into formatted string: %q", got)
	}
	if s.Reveal() != "hunter2" {
		t.Errorf("Reveal() = %q", s.Reveal())
	}
}

func TestEnvProvider_Resolve(t *testing.T) {
	t.Setenv("ROLLOPS_PETMED_PROD_DB_PASSWORD", "s3cr3t")
	p := EnvProvider{Prefix: "ROLLOPS_"}
	s, err := p.Resolve(context.Background(), "petmed/prod/db.password")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if s.Reveal() != "s3cr3t" {
		t.Errorf("got %q", s.Reveal())
	}
	if _, err := p.Resolve(context.Background(), "missing"); err == nil {
		t.Error("expected not-found for missing env")
	}
}

func TestChain_FirstHitWins(t *testing.T) {
	t.Setenv("B_KEY", "fromB")
	c := Chain{Providers: []Provider{
		EnvProvider{Prefix: "A_"}, // miss
		EnvProvider{Prefix: "B_"}, // hit
	}}
	s, err := c.Resolve(context.Background(), "key")
	if err != nil {
		t.Fatal(err)
	}
	if s.Reveal() != "fromB" {
		t.Errorf("chain resolved %q, want fromB", s.Reveal())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

func TestVaultProvider_Resolve(t *testing.T) {
	var gotToken, gotURL string
	v := VaultProvider{
		Addr:  "https://vault.internal",
		Token: "tkn",
		Client: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			gotToken = r.Header.Get("X-Vault-Token")
			gotURL = r.URL.String()
			body := `{"data":{"data":{"password":"vaultpass"}}}`
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		}),
	}
	s, err := v.Resolve(context.Background(), "kv/prod/db#password")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if s.Reveal() != "vaultpass" {
		t.Errorf("got %q", s.Reveal())
	}
	if gotToken != "tkn" {
		t.Errorf("token header = %q", gotToken)
	}
	if !strings.Contains(gotURL, "/v1/kv/data/prod/db") {
		t.Errorf("KV v2 path wrong: %q", gotURL)
	}
}

func TestVaultProvider_NotFound(t *testing.T) {
	v := VaultProvider{Addr: "https://v", Token: "t", Client: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})}
	if _, err := v.Resolve(context.Background(), "kv/x/y#k"); err == nil {
		t.Fatal("expected not-found")
	}
}

func TestFromEnv_UnsetIsEnvOnly(t *testing.T) {
	p, err := FromEnv(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.(EnvProvider); !ok {
		t.Fatalf("unset VAULT_ADDR must be EnvProvider, got %T", p)
	}
}

func TestFromEnv_VaultAddrBuildsChain(t *testing.T) {
	p, err := FromEnv(func(k string) string {
		switch k {
		case "VAULT_ADDR":
			return "https://vault.example"
		case "VAULT_TOKEN":
			return "s.xxx"
		default:
			return ""
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	c, ok := p.(Chain)
	if !ok || len(c.Providers) != 2 {
		t.Fatalf("got %T %#v", p, p)
	}
	v, ok := c.Providers[0].(VaultProvider)
	if !ok {
		t.Fatalf("first provider %T, want VaultProvider", c.Providers[0])
	}
	if v.Addr != "https://vault.example" || v.Token != "s.xxx" {
		t.Errorf("vault = %+v", v)
	}
	if _, ok := c.Providers[1].(EnvProvider); !ok {
		t.Errorf("second provider %T, want EnvProvider", c.Providers[1])
	}
}

func TestFromEnv_TokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(" filetok \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := FromEnv(func(k string) string {
		switch k {
		case "VAULT_ADDR":
			return "https://v"
		case "VAULT_TOKEN_FILE":
			return path
		default:
			return ""
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	c := p.(Chain)
	v := c.Providers[0].(VaultProvider)
	if v.Token != "filetok" {
		t.Errorf("token from file = %q", v.Token)
	}
}

func TestFromEnv_MissingTokenFileErrors(t *testing.T) {
	_, err := FromEnv(func(k string) string {
		switch k {
		case "VAULT_ADDR":
			return "https://v"
		case "VAULT_TOKEN_FILE":
			return filepath.Join(t.TempDir(), "missing")
		default:
			return ""
		}
	})
	if err == nil {
		t.Fatal("unreadable VAULT_TOKEN_FILE must error")
	}
}
