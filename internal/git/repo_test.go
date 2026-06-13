package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

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

func makeUpstream(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	gitRun(t, dir, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "rollops.yaml"), []byte("v1"), 0o644); err != nil {
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
	if _, err := os.Stat(filepath.Join(dest, "rollops.yaml")); err != nil {
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
	if err := os.WriteFile(filepath.Join(upstream, "rollops.yaml"), []byte("v2"), 0o644); err != nil {
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

func TestSource_CommitFile(t *testing.T) {
	upstream := makeUpstream(t)
	src := Open(upstream, "main", Auth{})
	before, err := src.Head(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	changed, head, err := src.CommitFile(context.Background(), "rollops.yaml", []byte("v2"), "chore: update image")
	if err != nil {
		t.Fatalf("CommitFile: %v", err)
	}
	if !changed || head == before {
		t.Fatalf("changed=%v head=%q before=%q", changed, head, before)
	}
	changed, _, err = src.CommitFile(context.Background(), "rollops.yaml", []byte("v2"), "chore: update image")
	if err != nil {
		t.Fatalf("CommitFile noop: %v", err)
	}
	if changed {
		t.Fatal("same content should not commit")
	}
	if _, _, err := src.CommitFile(context.Background(), "../escape", []byte("x"), "bad"); err == nil {
		t.Fatal("path escape should fail")
	}
}

func TestRedactArgs_MasksToken(t *testing.T) {
	args := []string{"-c", "http.extraheader=Authorization: Basic c2VjcmV0VG9rZW4=", "clone", "--depth", "1", "https://github.com/acme/x"}
	got := redactArgs(args)
	if strings.Contains(got, "c2VjcmV0VG9rZW4=") || strings.Contains(got, "Basic ") {
		t.Errorf("token not redacted: %q", got)
	}
	if !strings.Contains(got, "<redacted>") || !strings.Contains(got, "clone") {
		t.Errorf("redaction malformed: %q", got)
	}
}
