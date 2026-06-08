package reconcile

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/rolloffs/internal/audit"
	"go.klarlabs.de/rolloffs/internal/config"
	"go.klarlabs.de/rolloffs/internal/engine"
	"go.klarlabs.de/rolloffs/internal/git"
	"go.klarlabs.de/rolloffs/internal/rollout"
	"go.klarlabs.de/rolloffs/internal/store/sqlite"
	itarget "go.klarlabs.de/rolloffs/internal/target"
	pt "go.klarlabs.de/rolloffs/pkg/target"
)

const repoConfigV1 = `apiVersion: rolloffs.klarlabs.de/v1
kind: RolloutConfig
metadata:
  name: demo
spec:
  target:
    kind: fake
    ref: demo/prod/app
    criticality: low
    spec:
      x: 1
  strategy:
    type: rolling
`

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func makeRepo(t *testing.T, content string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	gitRun(t, dir, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "rolloffs.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-m", "init")
	return dir
}

func newWatcher(t *testing.T, fake *fakeTarget) *Watcher {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	reg := itarget.NewRegistry()
	reg.Register("fake", func(config.Target) (pt.Target, error) { return fake, nil })
	clock := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	id := 0
	eng := engine.New(db, reg, engine.WithClock(func() time.Time { return clock }), engine.WithIDGen(func() string { id++; return "ro" }))
	rec := New(eng, audit.New(io.Discard))
	return &Watcher{rec: rec, locks: newRepoLocks()}
}

func TestWatcher_TickReconcilesFromGit(t *testing.T) {
	upstream := makeRepo(t, repoConfigV1)
	fake := &fakeTarget{fp: pt.Fingerprint{Value: "stale"}} // drift vs desired
	w := newWatcher(t, fake)

	src, err := git.Clone(context.Background(), "file://"+upstream, "main", filepath.Join(t.TempDir(), "co"), git.Auth{})
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	w.AddExisting(RepoSpec{Name: "demo", Ref: config.RepoRef{Branch: "main", Path: "rolloffs.yaml"}, Initiator: rollout.Identity{Kind: "ci", Name: "watcher"}}, src)

	results := w.Tick(context.Background())
	if len(results) != 1 {
		t.Fatalf("want 1 repo outcome, got %d", len(results))
	}
	r := results[0]
	if r.Err != nil {
		t.Fatalf("tick err: %v", r.Err)
	}
	if !r.Outcome.Drift || !r.Outcome.Reconciled {
		t.Fatalf("expected drift+reconcile, got %+v", r.Outcome)
	}
	if len(fake.applied) != 1 {
		t.Errorf("watcher should reconcile drift once, applied=%d", len(fake.applied))
	}
}

func TestWatcher_PicksUpNewCommit(t *testing.T) {
	upstream := makeRepo(t, repoConfigV1)
	fake := &fakeTarget{}
	w := newWatcher(t, fake)

	src, err := git.Clone(context.Background(), "file://"+upstream, "main", filepath.Join(t.TempDir(), "co"), git.Auth{})
	if err != nil {
		t.Fatal(err)
	}
	w.AddExisting(RepoSpec{Name: "demo", Ref: config.RepoRef{Path: "rolloffs.yaml"}}, src)

	// First tick deploys (create); fake now stamps the v1 checksum.
	w.Tick(context.Background())
	afterFirst := len(fake.applied)

	// Change desired state upstream → new checksum → next tick must reconcile.
	v2 := strings.Replace(repoConfigV1, "x: 1", "x: 2", 1)
	if err := os.WriteFile(filepath.Join(upstream, "rolloffs.yaml"), []byte(v2), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, upstream, "commit", "-am", "v2")

	out := w.Tick(context.Background())
	if !out[0].Changed {
		t.Error("tick should detect the new commit")
	}
	if len(fake.applied) <= afterFirst {
		t.Errorf("new desired state should trigger a reconcile: %d -> %d", afterFirst, len(fake.applied))
	}
}
