package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func makeUpstream(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	gitRun(t, dir, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "rolloffs.yaml"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-m", "init")
	return dir
}

func TestClone_HeadAndPull(t *testing.T) {
	upstream := makeUpstream(t)
	dest := filepath.Join(t.TempDir(), "checkout")

	src, err := Clone(context.Background(), "file://"+upstream, "main", dest, Auth{})
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	head1, err := src.Head(context.Background())
	if err != nil || head1 == "" {
		t.Fatalf("Head: %v (%q)", err, head1)
	}
	if _, err := os.Stat(filepath.Join(dest, "rolloffs.yaml")); err != nil {
		t.Errorf("config not checked out: %v", err)
	}

	// No upstream change yet → Pull reports unchanged.
	changed, _, err := src.Pull(context.Background())
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if changed {
		t.Error("Pull should report unchanged when upstream has not moved")
	}

	// New upstream commit → Pull detects the change (drift heartbeat).
	if err := os.WriteFile(filepath.Join(upstream, "rolloffs.yaml"), []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, upstream, "commit", "-am", "update")

	changed, head2, err := src.Pull(context.Background())
	if err != nil {
		t.Fatalf("Pull after update: %v", err)
	}
	if !changed || head2 == head1 {
		t.Errorf("Pull should detect the new commit: changed=%v head2=%q", changed, head2)
	}
}
