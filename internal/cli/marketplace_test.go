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

// twoVersionServer serves an index for one plugin with an old (v0.1.0) and a
// latest (v0.2.0) release, both for the current platform, with the given pins.
func twoVersionServer(t *testing.T, oldSum, newSum string) (*http.Client, string) {
	t.Helper()
	body := fmt.Sprintf(`{"plugins":[{"name":"flagsmith","description":"d","capabilities":["featureflag"],"latest":"v0.2.0",
		"versions":{
		  "v0.1.0":{"artifacts":[{"os":%q,"arch":%q,"url":"https://artifact/0.1.0","sha256":%q}]},
		  "v0.2.0":{"artifacts":[{"os":%q,"arch":%q,"url":"https://artifact/0.2.0","sha256":%q}]}
		}}]}`, runtime.GOOS, runtime.GOARCH, oldSum, runtime.GOOS, runtime.GOARCH, newSum)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(body)) }))
	t.Cleanup(srv.Close)
	return srv.Client(), srv.URL
}

func sha(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

func TestPluginUpdate_ReportsOutdatedDryRun(t *testing.T) {
	oldContent, newContent := "old-binary", "new-binary"
	hc, url := twoVersionServer(t, sha(oldContent), sha(newContent))
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "flagsmith"), []byte(oldContent), 0o755); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	app := &App{Out: &buf, HTTPClient: hc}
	if err := app.Run(context.Background(), []string{"plugin", "update", "--dir", dir, "--registry", url}); err != nil {
		t.Fatalf("update: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "outdated (flagsmith v0.1.0 -> v0.2.0)") {
		t.Errorf("expected outdated report, got %q", out)
	}
	if !strings.Contains(out, "--apply") {
		t.Errorf("expected apply hint, got %q", out)
	}
	// Dry run must not change the binary.
	if got, _ := os.ReadFile(filepath.Join(dir, "flagsmith")); string(got) != oldContent {
		t.Error("dry run must not modify the binary")
	}
}

func TestPluginUpdate_ApplyUpgrades(t *testing.T) {
	oldContent, newContent := "old-binary", "new-binary"
	newSum := sha(newContent)
	hc, url := twoVersionServer(t, sha(oldContent), newSum)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "flagsmith"), []byte(oldContent), 0o755); err != nil {
		t.Fatal(err)
	}
	newBin := filepath.Join(t.TempDir(), "new")
	if err := os.WriteFile(newBin, []byte(newContent), 0o755); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	app := &App{
		Out:           &buf,
		HTTPClient:    hc,
		PluginFetcher: func(_ context.Context, _ string) (string, func(), error) { return newBin, func() {}, nil },
	}
	if err := app.Run(context.Background(), []string{"plugin", "update", "--dir", dir, "--apply", "--registry", url}); err != nil {
		t.Fatalf("update --apply: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "upgraded flagsmith to v0.2.0") || !strings.Contains(out, newSum) {
		t.Errorf("expected upgrade confirmation, got %q", out)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "flagsmith")); string(got) != newContent {
		t.Error("binary must be replaced with the new version on --apply")
	}
}

func TestPluginUpdate_UpToDateAndUnknown(t *testing.T) {
	newContent := "new-binary"
	hc, url := twoVersionServer(t, sha("old-binary"), sha(newContent))
	dir := t.TempDir()
	// Installed binary is already the latest.
	if err := os.WriteFile(filepath.Join(dir, "flagsmith"), []byte(newContent), 0o755); err != nil {
		t.Fatal(err)
	}
	// A binary not from this registry.
	if err := os.WriteFile(filepath.Join(dir, "homegrown"), []byte("local"), 0o755); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	app := &App{Out: &buf, HTTPClient: hc}
	if err := app.Run(context.Background(), []string{"plugin", "update", "--dir", dir, "--registry", url}); err != nil {
		t.Fatalf("update: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "flagsmith\tup to date (flagsmith v0.2.0)") {
		t.Errorf("expected up-to-date line, got %q", out)
	}
	if !strings.Contains(out, "homegrown\tunknown") {
		t.Errorf("expected unknown line, got %q", out)
	}
}

// signedRegistryServer serves both the index (at /) and a cosign bundle (at
// /bundle) for a signed, platform-matching artifact with the given pin.
func signedRegistryServer(t *testing.T, sum string) (*http.Client, string) {
	t.Helper()
	var base string
	mux := http.NewServeMux()
	mux.HandleFunc("/bundle", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("bundle-bytes")) })
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		body := fmt.Sprintf(`{"plugins":[{"name":"flagsmith","description":"d","capabilities":["featureflag"],"latest":"v0.1.0",
			"versions":{"v0.1.0":{"cosign":{"identity":"signer@klarlabs","issuer":"https://token.actions.githubusercontent.com"},
			"artifacts":[{"os":%q,"arch":%q,"url":"https://artifact/fs","sha256":%q,"bundle":%q}]}}}]}`,
			runtime.GOOS, runtime.GOARCH, sum, base+"/bundle")
		w.Write([]byte(body))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	base = srv.URL
	return srv.Client(), srv.URL
}

func TestPluginInstall_ByName_AutoCosignVerifies(t *testing.T) {
	content := "#!/bin/sh\necho fs\n"
	h := sha256.Sum256([]byte(content))
	sum := hex.EncodeToString(h[:])
	hc, url := signedRegistryServer(t, sum)
	dir := t.TempDir()

	bin := filepath.Join(t.TempDir(), "downloaded")
	if err := os.WriteFile(bin, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	var gotArgs []string
	var buf bytes.Buffer
	app := &App{
		Out:           &buf,
		HTTPClient:    hc,
		PluginFetcher: func(_ context.Context, _ string) (string, func(), error) { return bin, func() {}, nil },
		CosignRun: func(_ context.Context, _ string, args ...string) (string, error) {
			gotArgs = args
			return "Verified OK", nil
		},
	}
	if err := app.Run(context.Background(), []string{"plugin", "install", "flagsmith", "--dir", dir, "--registry", url}); err != nil {
		t.Fatalf("install: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "registry-published signature") || !strings.Contains(out, "cosign: verified") {
		t.Errorf("expected auto cosign verify, got %q", out)
	}
	// The verifier must use the registry's identity/issuer and the fetched bundle.
	joined := strings.Join(gotArgs, " ")
	for _, want := range []string{"--certificate-identity signer@klarlabs", "--certificate-oidc-issuer https://token.actions.githubusercontent.com", "--bundle"} {
		if !strings.Contains(joined, want) {
			t.Errorf("cosign args missing %q: %v", want, gotArgs)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "flagsmith")); err != nil {
		t.Fatalf("binary not installed: %v", err)
	}
}

func TestPluginInstall_ByName_AutoCosignFailureBlocks(t *testing.T) {
	content := "binary"
	h := sha256.Sum256([]byte(content))
	sum := hex.EncodeToString(h[:])
	hc, url := signedRegistryServer(t, sum)
	dir := t.TempDir()

	bin := filepath.Join(t.TempDir(), "downloaded")
	if err := os.WriteFile(bin, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	app := &App{
		Out:           &buf,
		HTTPClient:    hc,
		PluginFetcher: func(_ context.Context, _ string) (string, func(), error) { return bin, func() {}, nil },
		CosignRun: func(_ context.Context, _ string, _ ...string) (string, error) {
			return "no matching signatures", os.ErrInvalid
		},
	}
	err := app.Run(context.Background(), []string{"plugin", "install", "flagsmith", "--dir", dir, "--registry", url})
	if err == nil {
		t.Fatal("failed cosign verification must block install")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "flagsmith")); statErr == nil {
		t.Error("binary must not be installed when signature verification fails")
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
