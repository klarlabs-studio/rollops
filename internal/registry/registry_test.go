package registry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

const sampleIndex = `{
  "plugins": [
    {
      "name": "flagsmith",
      "description": "Feature-flag provider backed by Flagsmith.",
      "capabilities": ["featureflag"],
      "latest": "v0.2.0",
      "versions": {
        "v0.1.0": {
          "artifacts": [
            {"os": "linux", "arch": "amd64", "url": "https://x/fs_0.1.0_linux_amd64", "sha256": "aaa"}
          ]
        },
        "v0.2.0": {
          "cosign": {"identity": "https://github.com/klarlabs-studio/rollops-plugin-flagsmith/.github/workflows/release.yml@refs/tags/v0.2.0", "issuer": "https://token.actions.githubusercontent.com"},
          "artifacts": [
            {"os": "linux", "arch": "amd64", "url": "https://x/fs_0.2.0_linux_amd64", "sha256": "bbb"},
            {"os": "darwin", "arch": "arm64", "url": "https://x/fs_0.2.0_darwin_arm64", "sha256": "ccc"}
          ]
        }
      }
    },
    {
      "name": "unleash",
      "description": "Open-source feature toggles.",
      "capabilities": ["featureflag"],
      "latest": "v0.1.0",
      "versions": {"v0.1.0": {"artifacts": [{"os": "linux", "arch": "amd64", "url": "https://x/u", "sha256": "ddd"}]}}
    }
  ]
}`

func serveIndex(t *testing.T) (*http.Client, string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(sampleIndex))
	}))
	t.Cleanup(srv.Close)
	return srv.Client(), srv.URL
}

func TestFetchAndFind(t *testing.T) {
	hc, url := serveIndex(t)
	idx, err := Fetch(context.Background(), hc, url)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(idx.Plugins) != 2 {
		t.Fatalf("want 2 plugins, got %d", len(idx.Plugins))
	}
	if _, ok := idx.Find("flagsmith"); !ok {
		t.Error("flagsmith not found")
	}
	if _, ok := idx.Find("nope"); ok {
		t.Error("unexpected find for missing plugin")
	}
}

func TestFetchNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	if _, err := Fetch(context.Background(), srv.Client(), srv.URL); err == nil {
		t.Fatal("non-200 must error")
	}
}

func TestSearch(t *testing.T) {
	hc, url := serveIndex(t)
	idx, _ := Fetch(context.Background(), hc, url)

	all := idx.Search("")
	if len(all) != 2 || all[0].Name != "flagsmith" || all[1].Name != "unleash" {
		t.Fatalf("empty query must return all sorted by name, got %+v", all)
	}
	if m := idx.Search("FLAG"); len(m) != 2 {
		t.Fatalf("capability match (case-insensitive) want 2, got %d", len(m))
	}
	if m := idx.Search("unleash"); len(m) != 1 || m[0].Name != "unleash" {
		t.Fatalf("name match want unleash, got %+v", m)
	}
	if m := idx.Search("toggles"); len(m) != 1 || m[0].Name != "unleash" {
		t.Fatalf("description match want unleash, got %+v", m)
	}
	if m := idx.Search("zzz"); len(m) != 0 {
		t.Fatalf("no match want empty, got %+v", m)
	}
}

func TestResolve(t *testing.T) {
	hc, url := serveIndex(t)
	idx, _ := Fetch(context.Background(), hc, url)

	// Empty version uses latest, returns the platform artifact + cosign material.
	art, cos, err := idx.Resolve("flagsmith", "", "darwin", "arm64")
	if err != nil {
		t.Fatalf("resolve latest: %v", err)
	}
	if art.SHA256 != "ccc" || art.URL != "https://x/fs_0.2.0_darwin_arm64" {
		t.Errorf("wrong artifact: %+v", art)
	}
	if cos == nil || cos.Issuer != "https://token.actions.githubusercontent.com" {
		t.Errorf("expected cosign material, got %+v", cos)
	}

	// Explicit older version, unsigned.
	art, cos, err = idx.Resolve("flagsmith", "v0.1.0", "linux", "amd64")
	if err != nil {
		t.Fatalf("resolve pinned: %v", err)
	}
	if art.SHA256 != "aaa" {
		t.Errorf("wrong pinned artifact: %+v", art)
	}
	if cos != nil {
		t.Errorf("v0.1.0 is unsigned, got %+v", cos)
	}

	// Missing plugin, version, and platform each error.
	if _, _, err := idx.Resolve("nope", "", "linux", "amd64"); err == nil {
		t.Error("missing plugin must error")
	}
	if _, _, err := idx.Resolve("flagsmith", "v9.9.9", "linux", "amd64"); err == nil {
		t.Error("missing version must error")
	}
	if _, _, err := idx.Resolve("flagsmith", "v0.2.0", "windows", "386"); err == nil {
		t.Error("missing platform artifact must error")
	}
}

func TestFindVersionBySHA(t *testing.T) {
	hc, url := serveIndex(t)
	idx, _ := Fetch(context.Background(), hc, url)

	name, ver, ok := idx.FindVersionBySHA("aaa", "linux", "amd64")
	if !ok || name != "flagsmith" || ver != "v0.1.0" {
		t.Errorf("want flagsmith v0.1.0, got %s %s ok=%v", name, ver, ok)
	}
	// Case-insensitive on the hash.
	if _, ver, ok := idx.FindVersionBySHA("BBB", "linux", "amd64"); !ok || ver != "v0.2.0" {
		t.Errorf("want v0.2.0 for BBB, got %s ok=%v", ver, ok)
	}
	// Wrong platform must not match.
	if _, _, ok := idx.FindVersionBySHA("ccc", "linux", "amd64"); ok {
		t.Error("darwin artifact must not match linux/amd64")
	}
	// Unknown hash.
	if _, _, ok := idx.FindVersionBySHA("zzz", "linux", "amd64"); ok {
		t.Error("unknown sha must not match")
	}
}

func TestURLEnvOverride(t *testing.T) {
	t.Setenv("ROLLOPS_PLUGIN_REGISTRY", "https://example.test/index.json")
	if got := URL(); got != "https://example.test/index.json" {
		t.Errorf("env override ignored: %s", got)
	}
	t.Setenv("ROLLOPS_PLUGIN_REGISTRY", "")
	if got := URL(); got != DefaultURL {
		t.Errorf("want default, got %s", got)
	}
}
