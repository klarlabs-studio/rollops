package secrets

import (
	"context"
	"fmt"
	"io"
	"net/http"
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
