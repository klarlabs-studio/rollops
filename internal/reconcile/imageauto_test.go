package reconcile

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.klarlabs.de/rollops/internal/config"
	"go.klarlabs.de/rollops/internal/git"
)

type fakeTags []string

func (f fakeTags) Tags(context.Context, string) ([]string, error) { return f, nil }
func (f fakeTags) Digest(context.Context, string) (string, error) { return "sha256:deadbeef", nil }

type fakeDigest string

func (d fakeDigest) Tags(context.Context, string) ([]string, error) { return nil, nil }
func (d fakeDigest) Digest(context.Context, string) (string, error) { return string(d), nil }

// fakeRegistry maps each tag to its manifest digest, so Digest("repo:tag")
// resolves per-tag — what digest→semver migration needs.
type fakeRegistry struct {
	tags    []string
	digests map[string]string // tag -> digest
}

func (f fakeRegistry) Tags(context.Context, string) ([]string, error) { return f.tags, nil }
func (f fakeRegistry) Digest(_ context.Context, image string) (string, error) {
	_, tag := splitImage(image)
	return f.digests[tag], nil
}

const imgConfigYAML = `apiVersion: rollops.klarlabs.de/v1
kind: RolloutConfig
metadata:
  name: web
spec:
  target:
    kind: kubernetes
    ref: demo/prod/web
    criticality: low
    spec:
      namespace: demo
      resource: deployment/web
      image: ghcr.io/acme/web:v1.0.0
      manifest: |
        apiVersion: apps/v1
        kind: Deployment
        metadata: {name: web, namespace: demo}
        spec:
          template:
            spec:
              containers:
                - {name: web, image: ghcr.io/acme/web:v1.0.0}
  strategy:
    type: rolling
  imagePolicy:
    mode: minor
`

// newGitRepo creates a working tree with one committed config file and an origin
// it can push to (a bare repo), returning the Source.
func newGitRepo(t *testing.T, content string) *git.Source {
	t.Helper()
	const relPath = "apps/web.yaml"
	bare := t.TempDir()
	run(t, bare, "init", "--bare", "-b", "main")
	work := t.TempDir()
	run(t, work, "init", "-b", "main")
	run(t, work, "remote", "add", "origin", bare)
	if err := os.MkdirAll(filepath.Dir(filepath.Join(work, relPath)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, relPath), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, work, "add", ".")
	run(t, work, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-m", "init")
	run(t, work, "push", "-u", "origin", "main")
	return git.Open(work, "main", git.Auth{})
}

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

func TestImageAuto_BumpsAndCommits(t *testing.T) {
	src := newGitRepo(t, imgConfigYAML)
	cfg, err := config.Load([]byte(imgConfigYAML))
	if err != nil {
		t.Fatal(err)
	}
	ia := ImageAuto{Scanner: fakeTags{"v1.0.0", "v1.1.0", "v1.2.0", "v2.0.0"}}
	bumped, ref, err := ia.Process(context.Background(), src, config.NamedConfig{Path: "apps/web.yaml", Config: cfg})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	// minor mode → highest within major 1 = v1.2.0 (not v2.0.0).
	if ref != "ghcr.io/acme/web:v1.2.0" {
		t.Errorf("ref = %q, want ghcr.io/acme/web:v1.2.0", ref)
	}
	if got, _ := bumped.Spec.Target.Spec["image"].(string); got != "ghcr.io/acme/web:v1.2.0" {
		t.Errorf("bumped config image = %q", got)
	}
	// The file on disk was rewritten + committed.
	data, _ := os.ReadFile(filepath.Join(src.Dir(), "apps/web.yaml"))
	if !strings.Contains(string(data), "ghcr.io/acme/web:v1.2.0") {
		t.Errorf("committed file not bumped:\n%s", data)
	}
}

func TestImageAuto_NoPolicyNoop(t *testing.T) {
	cfg, _ := config.Load([]byte(strings.Replace(imgConfigYAML, "  imagePolicy:\n    mode: minor\n", "", 1)))
	_, ref, err := ImageAuto{Scanner: fakeTags{"v9.9.9"}}.Process(context.Background(), nil, config.NamedConfig{Path: "x", Config: cfg})
	if err != nil || ref != "" {
		t.Fatalf("no policy must be a no-op, got ref=%q err=%v", ref, err)
	}
}

func TestImageAuto_ModeNoneNoop(t *testing.T) {
	// mode: none disables automation — a digest-pinned image stays put even when
	// the scanner would otherwise resolve a different digest for the mutable tag.
	cfgYAML := strings.Replace(imgConfigYAML, "image: ghcr.io/acme/web:v1.0.0", "image: ghcr.io/acme/web:latest@sha256:pinned", 1)
	cfgYAML = strings.Replace(cfgYAML, "mode: minor", "mode: none", 1)
	src := newGitRepo(t, "apps/web.yaml", cfgYAML)
	cfg, err := config.Load([]byte(cfgYAML))
	if err != nil {
		t.Fatal(err)
	}
	bumped, ref, err := ImageAuto{Scanner: fakeDigest("sha256:newer")}.Process(context.Background(), src, config.NamedConfig{Path: "apps/web.yaml", Config: cfg})
	if err != nil || ref != "" {
		t.Fatalf("mode none must be a no-op, got ref=%q err=%v", ref, err)
	}
	if got, _ := bumped.Spec.Target.Spec["image"].(string); got != "ghcr.io/acme/web:latest@sha256:pinned" {
		t.Errorf("image must be unchanged, got %q", got)
	}
}

func TestImageAuto_DigestModePinsMutableTag(t *testing.T) {
	cfgYAML := strings.Replace(imgConfigYAML, "image: ghcr.io/acme/web:v1.0.0", "image: ghcr.io/acme/web:latest", 1)
	cfgYAML = strings.Replace(cfgYAML, "mode: minor", "mode: digest\n    allowMutableTags: true", 1)
	src := newGitRepo(t, cfgYAML)
	cfg, err := config.Load([]byte(cfgYAML))
	if err != nil {
		t.Fatal(err)
	}
	bumped, ref, err := ImageAuto{Scanner: fakeDigest("sha256:abc123")}.Process(context.Background(), src, config.NamedConfig{Path: "apps/web.yaml", Config: cfg})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if ref != "ghcr.io/acme/web:latest@sha256:abc123" {
		t.Errorf("ref = %q, want digest-pinned latest", ref)
	}
	if got, _ := bumped.Spec.Target.Spec["image"].(string); got != "ghcr.io/acme/web:latest@sha256:abc123" {
		t.Errorf("bumped image = %q", got)
	}
}

func TestImageAuto_DigestModeUnchanged(t *testing.T) {
	cfgYAML := strings.Replace(imgConfigYAML, "image: ghcr.io/acme/web:v1.0.0", "image: ghcr.io/acme/web:latest@sha256:same", 1)
	cfgYAML = strings.Replace(cfgYAML, "mode: minor", "mode: digest\n    allowMutableTags: true", 1)
	src := newGitRepo(t, cfgYAML)
	cfg, _ := config.Load([]byte(cfgYAML))
	_, ref, err := ImageAuto{Scanner: fakeDigest("sha256:same")}.Process(context.Background(), src, config.NamedConfig{Path: "apps/web.yaml", Config: cfg})
	if err != nil || ref != "" {
		t.Fatalf("same digest must be a no-op, got ref=%q err=%v", ref, err)
	}
}

func TestImageAuto_MigrateDigestToSemver(t *testing.T) {
	// Digest-pinned, no usable tag, under a semver policy → reverse-lookup the
	// semver tag whose digest matches, and rewrite to it.
	cfgYAML := strings.ReplaceAll(imgConfigYAML, "ghcr.io/acme/web:v1.0.0", "ghcr.io/acme/web@sha256:abc123")
	src := newGitRepo(t, cfgYAML)
	cfg, err := config.Load([]byte(cfgYAML))
	if err != nil {
		t.Fatal(err)
	}
	reg := fakeRegistry{
		tags: []string{"v1.0.0", "v1.1.0", "v1.2.0", "latest"},
		digests: map[string]string{
			"v1.0.0": "sha256:old0",
			"v1.1.0": "sha256:old1",
			"v1.2.0": "sha256:abc123", // the pinned digest
			"latest": "sha256:abc123",
		},
	}
	bumped, ref, err := ImageAuto{Scanner: reg}.Process(context.Background(), src, config.NamedConfig{Path: "apps/web.yaml", Config: cfg})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if ref != "ghcr.io/acme/web:v1.2.0" {
		t.Errorf("ref = %q, want ghcr.io/acme/web:v1.2.0", ref)
	}
	if got, _ := bumped.Spec.Target.Spec["image"].(string); got != "ghcr.io/acme/web:v1.2.0" {
		t.Errorf("migrated image = %q", got)
	}
}

func TestImageAuto_MigrateEmbeddedSemverTagStripsDigest(t *testing.T) {
	// repo:v1.1.0@sha256:… → trust the embedded semver tag, drop the digest. No
	// registry calls needed (nil scanner would panic if it tried).
	cfgYAML := strings.ReplaceAll(imgConfigYAML, "ghcr.io/acme/web:v1.0.0", "ghcr.io/acme/web:v1.1.0@sha256:abc123")
	src := newGitRepo(t, cfgYAML)
	cfg, _ := config.Load([]byte(cfgYAML))
	_, ref, err := ImageAuto{Scanner: fakeRegistry{}}.Process(context.Background(), src, config.NamedConfig{Path: "apps/web.yaml", Config: cfg})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if ref != "ghcr.io/acme/web:v1.1.0" {
		t.Errorf("ref = %q, want ghcr.io/acme/web:v1.1.0 (digest stripped)", ref)
	}
}

func TestImageAuto_MigrateNoMatchingDigest(t *testing.T) {
	// Pinned digest points at no published semver tag → error (best-effort), no bump.
	cfgYAML := strings.ReplaceAll(imgConfigYAML, "ghcr.io/acme/web:v1.0.0", "ghcr.io/acme/web@sha256:orphan")
	src := newGitRepo(t, cfgYAML)
	cfg, _ := config.Load([]byte(cfgYAML))
	reg := fakeRegistry{
		tags:    []string{"v1.0.0", "v1.1.0"},
		digests: map[string]string{"v1.0.0": "sha256:a", "v1.1.0": "sha256:b"},
	}
	_, ref, err := ImageAuto{Scanner: reg}.Process(context.Background(), src, config.NamedConfig{Path: "apps/web.yaml", Config: cfg})
	if err == nil {
		t.Fatal("expected error when no semver tag matches the pinned digest")
	}
	if ref != "" {
		t.Errorf("ref = %q, want empty on no match", ref)
	}
}

func TestImageAuto_AlreadyCurrent(t *testing.T) {
	src := newGitRepo(t, imgConfigYAML)
	cfg, _ := config.Load([]byte(imgConfigYAML))
	// Only the current tag available → no bump.
	_, ref, err := ImageAuto{Scanner: fakeTags{"v1.0.0"}}.Process(context.Background(), src, config.NamedConfig{Path: "apps/web.yaml", Config: cfg})
	if err != nil || ref != "" {
		t.Fatalf("already current must be no-op, got ref=%q err=%v", ref, err)
	}
}
