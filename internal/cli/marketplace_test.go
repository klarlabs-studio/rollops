package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// registryServer serves a one-plugin index whose only artifact matches the
// current runtime platform and carries the given sha256 pin.
func registryServer(t *testing.T, name, version, sum string) (*http.Client, string) {
	t.Helper()
	body := fmt.Sprintf(`{"plugins":[{"name":%q,"description":"test provider","capabilities":["featureflag"],"latest":%q,
		"versions":{%q:{"artifacts":[{"os":%q,"arch":%q,"url":"https://artifact/%s","sha256":%q}]}}}]}`,
		name, version, version, runtime.GOOS, runtime.GOARCH, name, sum)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.Client(), srv.URL
}

func TestPluginSearch_ListsMatches(t *testing.T) {
	hc, url := registryServer(t, "flagsmith", "v0.1.0", "deadbeef")
	var buf bytes.Buffer
	app := &App{Out: &buf, HTTPClient: hc}
	if err := app.Run(context.Background(), []string{"plugin", "search", "--registry", url, "flag"}); err != nil {
		t.Fatalf("search: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "flagsmith") || !strings.Contains(out, "v0.1.0") || !strings.Contains(out, "featureflag") {
		t.Errorf("search output missing plugin row: %q", out)
	}
}

func TestPluginSearch_NoMatches(t *testing.T) {
	hc, url := registryServer(t, "flagsmith", "v0.1.0", "deadbeef")
	var buf bytes.Buffer
	app := &App{Out: &buf, HTTPClient: hc}
	if err := app.Run(context.Background(), []string{"plugin", "search", "--registry", url, "zzz"}); err != nil {
		t.Fatalf("search: %v", err)
	}
	if !strings.Contains(buf.String(), "no plugins match") {
		t.Errorf("expected no-match message, got %q", buf.String())
	}
}

func TestPluginInstall_ByName_VerifiesPin(t *testing.T) {
	content := "#!/bin/sh\necho flagsmith\n"
	h := sha256.Sum256([]byte(content))
	sum := hex.EncodeToString(h[:])

	hc, url := registryServer(t, "flagsmith", "v0.1.0", sum)
	dir := t.TempDir()

	// The fetcher stands in for the artifact download, returning the binary
	// whose sha256 matches the registry pin.
	bin := filepath.Join(t.TempDir(), "downloaded")
	if err := os.WriteFile(bin, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	app := &App{
		Out:        &buf,
		HTTPClient: hc,
		PluginFetcher: func(_ context.Context, _ string) (string, func(), error) {
			return bin, func() {}, nil
		},
	}
	err := app.Run(context.Background(), []string{"plugin", "install", "flagsmith", "--dir", dir, "--registry", url})
	if err != nil {
		t.Fatalf("install by name: %v", err)
	}
	// Default install name is the registry plugin name.
	if _, err := os.Stat(filepath.Join(dir, "flagsmith")); err != nil {
		t.Fatalf("binary not installed under registry name: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "registry: sha256 matches the published pin") {
		t.Errorf("expected pin-match confirmation, got %q", out)
	}
}

func TestPluginInstall_ByName_PinMismatchRejects(t *testing.T) {
	hc, url := registryServer(t, "flagsmith", "v0.1.0", "0000000000000000000000000000000000000000000000000000000000000000")
	dir := t.TempDir()

	bin := filepath.Join(t.TempDir(), "downloaded")
	if err := os.WriteFile(bin, []byte("tampered"), 0o755); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	app := &App{
		Out:        &buf,
		HTTPClient: hc,
		PluginFetcher: func(_ context.Context, _ string) (string, func(), error) {
			return bin, func() {}, nil
		},
	}
	err := app.Run(context.Background(), []string{"plugin", "install", "flagsmith", "--dir", dir, "--registry", url})
	if err == nil {
		t.Fatal("pin mismatch must block install")
	}
	if !strings.Contains(err.Error(), "registry pin mismatch") {
		t.Errorf("expected pin-mismatch error, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "flagsmith")); statErr == nil {
		t.Error("binary must not be installed on pin mismatch")
	}
}

func TestPluginInstall_ByName_UnknownPlugin(t *testing.T) {
	hc, url := registryServer(t, "flagsmith", "v0.1.0", "deadbeef")
	var buf bytes.Buffer
	app := &App{Out: &buf, HTTPClient: hc}
	err := app.Run(context.Background(), []string{"plugin", "install", "nonexistent", "--registry", url})
	if err == nil {
		t.Fatal("unknown plugin name must error")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected not-found error, got %v", err)
	}
}
