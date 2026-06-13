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

func TestPluginInfo_PrintsVersionsAndPins(t *testing.T) {
	body := `{"plugins":[{"name":"flagsmith","description":"FF provider","homepage":"https://example/fs","capabilities":["featureflag"],"latest":"v0.2.0",
		"versions":{
		  "v0.1.0":{"artifacts":[{"os":"linux","arch":"amd64","url":"https://x/0.1.0","sha256":"aaa"}]},
		  "v0.2.0":{"cosign":{"identity":"id-x","issuer":"iss-x"},"artifacts":[{"os":"linux","arch":"amd64","url":"https://x/0.2.0","sha256":"bbb"}]}
		}}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(body)) }))
	defer srv.Close()

	var buf bytes.Buffer
	app := &App{Out: &buf, HTTPClient: srv.Client()}
	if err := app.Run(context.Background(), []string{"plugin", "info", "flagsmith", "--registry", srv.URL}); err != nil {
		t.Fatalf("info: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"flagsmith", "https://example/fs", "featureflag", "v0.1.0", "v0.2.0", "aaa", "bbb", "id-x", "iss-x"} {
		if !strings.Contains(out, want) {
			t.Errorf("info output missing %q:\n%s", want, out)
		}
	}
	// Latest must be listed before the older version.
	if strings.Index(out, "v0.2.0") > strings.Index(out, "v0.1.0") {
		t.Errorf("latest version must be listed first:\n%s", out)
	}
}

func TestPluginInfo_UnknownPlugin(t *testing.T) {
	hc, url := registryServer(t, "flagsmith", "v0.1.0", "deadbeef")
	app := &App{Out: &bytes.Buffer{}, HTTPClient: hc}
	if err := app.Run(context.Background(), []string{"plugin", "info", "nope", "--registry", url}); err == nil {
		t.Fatal("unknown plugin must error")
	}
}

func TestPluginInfo_RequiresName(t *testing.T) {
	hc, url := registryServer(t, "flagsmith", "v0.1.0", "deadbeef")
	app := &App{Out: &bytes.Buffer{}, HTTPClient: hc}
	if err := app.Run(context.Background(), []string{"plugin", "info", "--registry", url}); err == nil {
		t.Fatal("missing name must error")
	}
}

func TestPluginList_ShowsInstalledPins(t *testing.T) {
	dir := t.TempDir()
	content := "#!/bin/sh\necho hi\n"
	h := sha256.Sum256([]byte(content))
	sum := hex.EncodeToString(h[:])
	if err := os.WriteFile(filepath.Join(dir, "flagsmith"), []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	// A non-executable file in the dir must be skipped.
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("notes"), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	app := &App{Out: &buf}
	if err := app.Run(context.Background(), []string{"plugin", "list", "--dir", dir}); err != nil {
		t.Fatalf("list: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "flagsmith") || !strings.Contains(out, sum) {
		t.Errorf("list missing plugin/pin: %q", out)
	}
	if strings.Contains(out, "README") {
		t.Errorf("non-executable file must be skipped: %q", out)
	}
}

func TestPluginList_EmptyDir(t *testing.T) {
	var buf bytes.Buffer
	app := &App{Out: &buf}
	if err := app.Run(context.Background(), []string{"plugin", "list", "--dir", t.TempDir()}); err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(buf.String(), "no plugins installed") {
		t.Errorf("expected empty message, got %q", buf.String())
	}
}

func TestPluginList_MissingDir(t *testing.T) {
	var buf bytes.Buffer
	app := &App{Out: &buf}
	missing := filepath.Join(t.TempDir(), "nope")
	if err := app.Run(context.Background(), []string{"plugin", "list", "--dir", missing}); err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(buf.String(), "does not exist") {
		t.Errorf("expected missing-dir message, got %q", buf.String())
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
