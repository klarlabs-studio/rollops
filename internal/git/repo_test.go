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

func TestVerifyHeadSignature_UnsignedFails(t *testing.T) {
	// DESIGN (finding #6): an unsigned HEAD commit fails verification. (The signed
	// happy path needs a GPG/SSH signing key, impractical in CI — the security
	// assertion is that unsigned/unverifiable commits are rejected.)
	upstream := makeUpstream(t)
	dest := filepath.Join(t.TempDir(), "checkout")
	src, err := Clone(context.Background(), "file://"+upstream, "main", dest, Auth{})
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	if err := src.VerifyHeadSignature(context.Background()); err == nil {
		t.Fatal("unsigned HEAD must fail signature verification")
	}
}

func TestPull_RequireSignedCommits(t *testing.T) {
	upstream := makeUpstream(t)
	dest := filepath.Join(t.TempDir(), "checkout")
	src, err := Clone(context.Background(), "file://"+upstream, "main", dest, Auth{})
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}

	// Default (env unset) → Pull ignores signatures and succeeds on unsigned HEAD.
	if _, _, err := src.Pull(context.Background()); err != nil {
		t.Fatalf("Pull (default off): %v", err)
	}

	// Enabled → Pull fails closed on the unsigned HEAD (non-fatal to other repos).
	t.Setenv("ROLLOPS_REQUIRE_SIGNED_COMMITS", "1")
	if _, _, err := src.Pull(context.Background()); err == nil {
		t.Fatal("Pull must fail when signed commits are required and HEAD is unsigned")
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

func TestCommitFileOnBranch_RefusesTrackedBranch(t *testing.T) {
	s := Open(t.TempDir(), "main", Auth{})
	if _, err := s.CommitFileOnBranch(context.Background(), "main", "x.yaml", []byte("y"), "m"); err == nil {
		t.Fatal("committing the PR path onto the tracked branch must be refused")
	}
	if _, err := s.CommitFileOnBranch(context.Background(), "", "x.yaml", []byte("y"), "m"); err == nil {
		t.Fatal("empty head branch must be refused")
	}
}

// TestCommitFileOnBranch_UnchangedProposalIsNotRebuilt covers the loop that
// starved a PR's CI in practice: rollops re-proposed the same image bump every
// poll, each force-push cancelling the in-flight checks, so the slowest job
// never reported and the PR could never merge. Three Recall Gate runs passed in
// four minutes on klarlabs-studio/mnemos#267 while `ci / Test` was killed each
// time; the two newest commits carried byte-identical diffs under different
// shas.
//
// The caller already guards on !committed. The bug was here: rebuilding the
// head branch from the tracked branch before comparing meant the comparison ran
// against a tree that never contains the bump, so it always "changed".
func TestCommitFileOnBranch_UnchangedProposalIsNotRebuilt(t *testing.T) {
	upstream := makeUpstream(t)
	dest := filepath.Join(t.TempDir(), "checkout")
	src, err := Clone(context.Background(), "file://"+upstream, "main", dest, Auth{})
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	ctx := context.Background()
	bump := []byte("image: repo@sha256:aaaa\n")

	// First proposal: a real change, so it commits and would be pushed.
	committed, err := src.CommitFileOnBranch(ctx, "rollops/image/x", "rollops.yaml", bump, "chore(image): bump")
	if err != nil {
		t.Fatalf("first proposal: %v", err)
	}
	if !committed {
		t.Fatal("first proposal did not commit — nothing would be proposed at all")
	}
	if err := src.PushBranch(ctx, "rollops/image/x"); err != nil {
		t.Fatalf("PushBranch: %v", err)
	}
	first, err := src.git(ctx, src.dir, "rev-parse", "rollops/image/x")
	if err != nil {
		t.Fatal(err)
	}

	// Second poll, same digest: must be a no-op. Re-committing here is what
	// force-pushes a fresh sha and cancels the PR's checks.
	committed, err = src.CommitFileOnBranch(ctx, "rollops/image/x", "rollops.yaml", bump, "chore(image): bump")
	if err != nil {
		t.Fatalf("second proposal: %v", err)
	}
	if committed {
		t.Error("unchanged bump was re-committed — every poll republishes an identical diff under a new sha, starving the PR's CI")
	}
	second, err := src.git(ctx, src.dir, "rev-parse", "rollops/image/x")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Errorf("proposal branch moved for an unchanged bump: %s -> %s", first[:12], second[:12])
	}

	// A genuinely new digest must still propose, and must land on current main.
	next := []byte("image: repo@sha256:bbbb\n")
	if committed, err = src.CommitFileOnBranch(ctx, "rollops/image/x", "rollops.yaml", next, "chore(image): bump"); err != nil || !committed {
		t.Fatalf("a real change must still be proposed: committed=%v err=%v", committed, err)
	}
}
