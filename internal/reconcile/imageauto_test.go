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
func newGitRepo(t *testing.T, relPath, content string) *git.Source {
	t.Helper()
	bare := t.TempDir()
	run(t, bare, "git", "init", "--bare", "-b", "main")
	work := t.TempDir()
	run(t, work, "git", "init", "-b", "main")
	run(t, work, "git", "remote", "add", "origin", bare)
	if err := os.MkdirAll(filepath.Dir(filepath.Join(work, relPath)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, relPath), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, work, "git", "add", ".")
	run(t, work, "git", "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-m", "init")
	run(t, work, "git", "push", "-u", "origin", "main")
	return git.Open(work, "main", git.Auth{})
}

func run(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v: %s", name, strings.Join(args, " "), err, out)
	}
}

func TestImageAuto_BumpsAndCommits(t *testing.T) {
	src := newGitRepo(t, "apps/web.yaml", imgConfigYAML)
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

func TestImageAuto_AlreadyCurrent(t *testing.T) {
	src := newGitRepo(t, "apps/web.yaml", imgConfigYAML)
	cfg, _ := config.Load([]byte(imgConfigYAML))
	// Only the current tag available → no bump.
	_, ref, err := ImageAuto{Scanner: fakeTags{"v1.0.0"}}.Process(context.Background(), src, config.NamedConfig{Path: "apps/web.yaml", Config: cfg})
	if err != nil || ref != "" {
		t.Fatalf("already current must be no-op, got ref=%q err=%v", ref, err)
	}
}
