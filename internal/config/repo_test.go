package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepoRef_Defaults(t *testing.T) {
	r := RepoRef{URL: "git@github.com:acme/api.git"}.WithDefaults()
	if r.Branch != DefaultBranch || r.Path != DefaultPath {
		t.Errorf("defaults not applied: %+v", r)
	}
	custom := RepoRef{Branch: "release", Path: "deploy/rollops.yaml"}.WithDefaults()
	if custom.Branch != "release" || custom.Path != "deploy/rollops.yaml" {
		t.Errorf("explicit values overwritten: %+v", custom)
	}
}

func TestLoadFromDir_ResolvesPathAndValidates(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "deploy")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "rollops.yaml"), []byte(validYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := LoadFromDir(dir, RepoRef{Path: "deploy/rollops.yaml"})
	if err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}
	if c.Metadata.Name == "" {
		t.Error("config not loaded")
	}
}

func TestLoadAllFromDir_DirectoryOfConfigs(t *testing.T) {
	dir := t.TempDir()
	apps := filepath.Join(dir, "apps")
	if err := os.MkdirAll(apps, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"a.yaml", "b.yaml", "ignore.txt"} {
		if err := os.WriteFile(filepath.Join(apps, n), []byte(validYAML), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := LoadAllFromDir(dir, RepoRef{Path: "apps"})
	if err != nil {
		t.Fatalf("LoadAllFromDir: %v", err)
	}
	if len(got) != 2 { // a.yaml + b.yaml, ignore.txt skipped
		t.Fatalf("loaded %d configs, want 2", len(got))
	}
	if got[0].Path != "apps/a.yaml" {
		t.Errorf("path = %q, want apps/a.yaml", got[0].Path)
	}
}

func TestLoadAllFromDir_SingleFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rollops.yaml"), []byte(validYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadAllFromDir(dir, RepoRef{Path: "rollops.yaml"})
	if err != nil || len(got) != 1 {
		t.Fatalf("single file: got %d configs, err %v", len(got), err)
	}
}

func TestLoadAllFromDir_EmptyDirErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAllFromDir(dir, RepoRef{Path: "empty"}); err == nil {
		t.Fatal("empty config dir must error")
	}
}

func TestLoadFromDir_MissingFile(t *testing.T) {
	if _, err := LoadFromDir(t.TempDir(), RepoRef{}); err == nil {
		t.Fatal("expected error for missing config file")
	}
}

func TestLoadFromDir_RejectsSymlink(t *testing.T) {
	// SECURITY (finding #2): a watched repo must not be able to redirect a config
	// read to an arbitrary host path/device via a symlink (e.g. → /dev/zero).
	dir := t.TempDir()
	target := filepath.Join(dir, "real.yaml")
	if err := os.WriteFile(target, []byte(validYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "rollops.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, err := LoadFromDir(dir, RepoRef{}); err == nil {
		t.Fatal("expected symlink config to be rejected")
	}
	// The directory walk must reject a symlinked entry too.
	if _, err := LoadAllFromDir(dir, RepoRef{Path: "."}); err == nil {
		t.Fatal("expected symlinked config in a dir to be rejected")
	}
}

func TestLoadFromDir_RejectsOversizeFile(t *testing.T) {
	// SECURITY (finding #2): an oversized config blob must not be read into memory
	// unbounded (OOM). The read is capped at maxConfigBytes.
	dir := t.TempDir()
	big := make([]byte, maxConfigBytes+1)
	for i := range big {
		big[i] = 'a'
	}
	if err := os.WriteFile(filepath.Join(dir, "rollops.yaml"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadFromDir(dir, RepoRef{})
	if err == nil {
		t.Fatal("expected oversize config to be rejected")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("err = %v, want a size-cap error", err)
	}
}

func TestReadConfigFile_ReadsRegularFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ok.yaml")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := readConfigFile(path)
	if err != nil || string(data) != "hello" {
		t.Fatalf("readConfigFile: %q err %v", data, err)
	}
}

func TestLoadFromDir_RejectsInvalidConfig(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rollops.yaml"), []byte("apiVersion: rollops.klarlabs.de/v1\nkind: RolloutConfig\nmetadata: {}\nspec: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFromDir(dir, RepoRef{}); err == nil {
		t.Fatal("expected validation error for incomplete config")
	}
}
