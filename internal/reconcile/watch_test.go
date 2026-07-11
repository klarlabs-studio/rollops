package reconcile

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.klarlabs.de/rollops/internal/audit"
	"go.klarlabs.de/rollops/internal/config"
	"go.klarlabs.de/rollops/internal/engine"
	"go.klarlabs.de/rollops/internal/git"
	"go.klarlabs.de/rollops/internal/rollout"
	"go.klarlabs.de/rollops/internal/store/sqlite"
	itarget "go.klarlabs.de/rollops/internal/target"
	pt "go.klarlabs.de/rollops/pkg/target"
)

const repoConfigV1 = `apiVersion: rollops.klarlabs.de/v1
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
	base := []string{"-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false"}
	cmd := exec.Command("git", append(base, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "HOME="+t.TempDir())
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
	if err := os.WriteFile(filepath.Join(dir, "rollops.yaml"), []byte(content), 0o644); err != nil {
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
	t.Cleanup(func() { _ = db.Close() })
	reg := itarget.NewRegistry()
	reg.Register("fake", func(config.Target) (pt.Target, error) { return fake, nil })
	clock := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	id := 0
	eng := engine.New(db, reg, engine.WithClock(func() time.Time { return clock }), engine.WithIDGen(func() string { id++; return "ro" }))
	rec := New(eng, audit.New(io.Discard))
	return &Watcher{rec: rec, locks: newRepoLocks()}
}

func TestWatcher_LeaderElectionSkipsNonLeader(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "leader.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	w1 := &Watcher{locks: newRepoLocks(), leases: db, owner: "one", leaseTTL: time.Minute, now: func() time.Time { return now }}
	w2 := &Watcher{locks: newRepoLocks(), leases: db, owner: "two", leaseTTL: time.Minute, now: func() time.Time { return now.Add(time.Second) }}

	if out := w1.Tick(context.Background()); len(out) != 0 {
		t.Fatalf("leader with no repos should produce no outcomes, got %+v", out)
	}
	out := w2.Tick(context.Background())
	if len(out) != 1 || out[0].Err != ErrNotLeader {
		t.Fatalf("non-leader outcome = %+v", out)
	}
	w2.now = func() time.Time { return now.Add(2 * time.Minute) }
	if out := w2.Tick(context.Background()); len(out) != 0 {
		t.Fatalf("expired leader lease should be acquirable, got %+v", out)
	}
}

func TestWatcher_TickReconcilesFromGit(t *testing.T) {
	upstream := makeRepo(t, repoConfigV1)
	fake := &fakeTarget{fp: pt.Fingerprint{Value: "stale"}} // drift vs desired
	w := newWatcher(t, fake)

	src, err := git.Clone(context.Background(), "file://"+upstream, "main", filepath.Join(t.TempDir(), "co"), git.Auth{})
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	w.AddExisting(RepoSpec{Name: "demo", Ref: config.RepoRef{Branch: "main", Path: "rollops.yaml"}, Initiator: rollout.Identity{Kind: "ci", Name: "watcher"}}, src)

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
	w.AddExisting(RepoSpec{Name: "demo", Ref: config.RepoRef{Path: "rollops.yaml"}}, src)

	// First tick deploys (create); fake now stamps the v1 checksum.
	w.Tick(context.Background())
	afterFirst := len(fake.applied)

	// Change desired state upstream → new checksum → next tick must reconcile.
	v2 := strings.Replace(repoConfigV1, "x: 1", "x: 2", 1)
	if err := os.WriteFile(filepath.Join(upstream, "rollops.yaml"), []byte(v2), 0o644); err != nil {
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

// orderLog records the interleaving of image scans and target applies across a
// tick, so a test can assert image automation for EVERY config runs before ANY
// reconcile (the decoupling that stops a blocked rollout starving another app's
// bump). Guarded because a reconcile could, in principle, run concurrently.
type orderLog struct {
	mu     sync.Mutex
	events []string
}

func (o *orderLog) add(e string) { o.mu.Lock(); o.events = append(o.events, e); o.mu.Unlock() }

// fakeScanner is a TagLister offering one newer minor tag (v1.0.0 -> v1.1.0) and
// recording each scan in the shared order log.
type fakeScanner struct{ log *orderLog }

func (f *fakeScanner) Tags(_ context.Context, _ string) ([]string, error) {
	f.log.add("scan")
	return []string{"v1.0.0", "v1.1.0"}, nil
}
func (f *fakeScanner) Digest(_ context.Context, _ string) (string, error) {
	return "sha256:" + strings.Repeat("a", 64), nil
}

// orderTarget is a fakeTarget that records each apply in the shared order log.
type orderTarget struct {
	fakeTarget
	log *orderLog
}

func (o *orderTarget) Apply(ctx context.Context, m pt.Manifest) (pt.Result, error) {
	o.log.add("apply")
	return o.fakeTarget.Apply(ctx, m)
}

func imgConfig(name, image string) string {
	return `apiVersion: rollops.klarlabs.de/v1
kind: RolloutConfig
metadata:
  name: ` + name + `
spec:
  target:
    kind: fake
    ref: demo/prod/` + name + `
    criticality: low
    spec:
      x: 1
      image: ` + image + `
  strategy:
    type: rolling
  imagePolicy:
    mode: minor
`
}

func makeRepoFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	gitRun(t, dir, "init", "-b", "main")
	// Let a push to the checked-out branch update the working tree, so image
	// automation's commit+push to this file:// remote succeeds in the test.
	gitRun(t, dir, "config", "receive.denyCurrentBranch", "updateInstead")
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-m", "init")
	return dir
}

// TestWatcher_ImageAutoDecoupledFromReconcile guards the fix: image automation for
// EVERY config must run before ANY reconcile in the same tick. Interleaving them
// (bump→deploy per config) let a slow/blocked progressive rollout for one app
// starve the image bumps of the apps after it in the loop — so a published tag
// sat undeployed indefinitely (observed: one app never auto-bumped). The order log
// makes the regression detectable even though the fake reconcile is instant: on the
// old code the order is scan,apply,scan,apply; the assertion below requires both
// scans before any apply.
func TestWatcher_ImageAutoDecoupledFromReconcile(t *testing.T) {
	upstream := makeRepoFiles(t, map[string]string{
		"app-a.yaml": imgConfig("app-a", "ghcr.io/acme/app-a:v1.0.0"),
		"app-b.yaml": imgConfig("app-b", "ghcr.io/acme/app-b:v1.0.0"),
	})

	log := &orderLog{}
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := itarget.NewRegistry()
	reg.Register("fake", func(config.Target) (pt.Target, error) { return &orderTarget{log: log}, nil })
	clock := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	id := 0
	eng := engine.New(db, reg, engine.WithClock(func() time.Time { return clock }), engine.WithIDGen(func() string { id++; return "ro" }))
	w := &Watcher{rec: New(eng, audit.New(io.Discard)), locks: newRepoLocks(), imageAuto: &ImageAuto{Scanner: &fakeScanner{log: log}}}

	src, err := git.Clone(context.Background(), "file://"+upstream, "main", filepath.Join(t.TempDir(), "co"), git.Auth{})
	if err != nil {
		t.Fatal(err)
	}
	w.AddExisting(RepoSpec{Name: "demo", Ref: config.RepoRef{Branch: "main", Path: "."}}, src)

	if out := w.Tick(context.Background()); len(out) != 2 {
		t.Fatalf("want 2 config outcomes, got %d: %+v", len(out), out)
	}

	// Every scan must precede every apply (decoupled phases). On the interleaved
	// code an apply appears before the second scan.
	firstApply, lastScan := -1, -1
	for i, e := range log.events {
		if e == "apply" && firstApply == -1 {
			firstApply = i
		}
		if e == "scan" {
			lastScan = i
		}
	}
	if got := len(log.events); got < 4 {
		t.Fatalf("want >=4 events (2 scan, 2 apply), got %d: %v", got, log.events)
	}
	if firstApply != -1 && firstApply < lastScan {
		t.Errorf("a reconcile ran before all image bumps — image automation is not decoupled: %v", log.events)
	}
	// And both configs really were bumped in Git.
	for _, name := range []string{"app-a.yaml", "app-b.yaml"} {
		data, _ := os.ReadFile(filepath.Join(src.Dir(), name))
		if !strings.Contains(string(data), "v1.1.0") {
			t.Errorf("%s not bumped to v1.1.0:\n%s", name, data)
		}
	}
}

// TestWatcher_ImageAutoLogsCoverageSummary asserts a per-tick summary names EVERY
// config and its image-automation decision, so a starved/stuck target is visible
// instead of silently indistinguishable from an up-to-date one.
func TestWatcher_ImageAutoLogsCoverageSummary(t *testing.T) {
	upstream := makeRepoFiles(t, map[string]string{
		"app-a.yaml": imgConfig("app-a", "ghcr.io/acme/app-a:v1.0.0"),
		"app-b.yaml": imgConfig("app-b", "ghcr.io/acme/app-b:v1.0.0"),
	})

	var logmu sync.Mutex
	var logs []string
	logf := func(f string, a ...any) { logmu.Lock(); logs = append(logs, fmt.Sprintf(f, a...)); logmu.Unlock() }

	db, err := sqlite.Open(filepath.Join(t.TempDir(), "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := itarget.NewRegistry()
	reg.Register("fake", func(config.Target) (pt.Target, error) { return &fakeTarget{}, nil })
	clock := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	id := 0
	eng := engine.New(db, reg, engine.WithClock(func() time.Time { return clock }), engine.WithIDGen(func() string { id++; return "ro" }))
	w := &Watcher{rec: New(eng, audit.New(io.Discard)), locks: newRepoLocks(), imageAuto: &ImageAuto{Scanner: &fakeScanner{log: &orderLog{}}}, logf: logf}

	src, err := git.Clone(context.Background(), "file://"+upstream, "main", filepath.Join(t.TempDir(), "co"), git.Auth{})
	if err != nil {
		t.Fatal(err)
	}
	w.AddExisting(RepoSpec{Name: "demo", Ref: config.RepoRef{Branch: "main", Path: "."}}, src)
	w.Tick(context.Background())

	var summary string
	for _, l := range logs {
		if strings.HasPrefix(l, "image automation demo:") && strings.Contains(l, "config(s)") {
			summary = l
		}
	}
	if summary == "" {
		t.Fatalf("no image-automation coverage summary logged; logs: %v", logs)
	}
	if !strings.Contains(summary, "2 config(s)") {
		t.Errorf("summary should count 2 configs: %q", summary)
	}
	for _, name := range []string{"app-a", "app-b"} {
		if !strings.Contains(summary, name) {
			t.Errorf("coverage summary must name %s (else a starved target is invisible): %q", name, summary)
		}
	}
}
