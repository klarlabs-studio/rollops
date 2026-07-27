package git

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// TokenProvider resolves an https token at command time. It lets short-lived,
// auto-rotating credentials (GitHub App installation tokens) be minted and
// refreshed on demand instead of pinned at startup. Implementations must be
// safe for concurrent use and cache until near expiry.
type TokenProvider func(ctx context.Context) (string, error)

// Auth carries per-repo credentials. Tokens/keys come from the SecretProvider
// at execution time, never stored locally by Rollops.
type Auth struct {
	// DeployKeyPath is an SSH private key path for git+ssh remotes.
	DeployKeyPath string
	// Token is a static GitHub App installation / PAT token for https remotes.
	Token string
	// TokenSource, when set, resolves the https token per git command and takes
	// precedence over Token — the seam for short-lived, rotating credentials.
	TokenSource TokenProvider
}

// token resolves the https token for one command: the dynamic source when set,
// else the static token.
func (a Auth) token(ctx context.Context) (string, error) {
	if a.TokenSource != nil {
		return a.TokenSource(ctx)
	}
	return a.Token, nil
}

// Source is a checked-out working tree for one repo at one branch.
type Source struct {
	dir    string
	branch string
	auth   Auth
	url    string // remote URL, retained for the pull-request API (owner/repo)
	// apiBase is the GitHub REST/GraphQL host, overridable in tests. Empty means
	// the public default (https://api.github.com).
	apiBase string
}

// Clone checks out url@branch into dir. Each watched repo gets its own Source —
// isolation is a property of Git structure.
func Clone(ctx context.Context, url, branch, dir string, auth Auth) (*Source, error) {
	s := &Source{dir: dir, branch: branch, auth: auth, url: url}
	args := []string{"clone", "--depth", "1", "--branch", branch, url, dir}
	if _, err := s.git(ctx, "", args...); err != nil {
		return nil, fmt.Errorf("git: clone %s@%s: %w", url, branch, err)
	}
	return s, nil
}

// Open wraps an existing working tree (already cloned/checked out).
func Open(dir, branch string, auth Auth) *Source {
	return &Source{dir: dir, branch: branch, auth: auth}
}

// WithURL sets the remote URL on a Source built by Open (Clone sets it already).
// It is needed for pull-request writeback, which derives owner/repo from the URL.
func (s *Source) WithURL(url string) *Source { s.url = url; return s }

// WithAPIBase overrides the GitHub API host (tests point it at a stub server).
func (s *Source) WithAPIBase(base string) *Source { s.apiBase = base; return s }

// Branch is the tracked branch this Source follows.
func (s *Source) Branch() string { return s.branch }

// Dir is the working-tree path where the config is read from.
func (s *Source) Dir() string { return s.dir }

// Head returns the current HEAD commit SHA.
func (s *Source) Head(ctx context.Context) (string, error) {
	out, err := s.git(ctx, s.dir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// Pull fetches the branch and fast-forwards the working tree, reporting whether
// HEAD moved. The poll loop calls this: a changed head triggers reconciliation,
// and the call doubles as the drift heartbeat.
//
// When ROLLOPS_REQUIRE_SIGNED_COMMITS is enabled, the checked-out HEAD's commit
// signature is verified after the reset; a failure returns an error so the
// caller skips reconciling this repo (non-fatal to other repos) rather than
// deploying an unverified commit. rollops follows a mutable branch via
// reset --hard, so this is the opt-in equivalent of Flux/Argo GPG verification.
func (s *Source) Pull(ctx context.Context) (changed bool, head string, err error) {
	before, _ := s.Head(ctx)
	if _, err := s.git(ctx, s.dir, "fetch", "origin", s.branch); err != nil {
		return false, before, err
	}
	if _, err := s.git(ctx, s.dir, "reset", "--hard", "origin/"+s.branch); err != nil {
		return false, before, err
	}
	after, err := s.Head(ctx)
	if err != nil {
		return false, before, err
	}
	if requireSignedCommits() {
		if verr := s.VerifyHeadSignature(ctx); verr != nil {
			return before != after, after, verr
		}
	}
	return before != after, after, nil
}

// VerifyHeadSignature runs `git verify-commit HEAD`, returning a non-nil error
// when the HEAD commit is unsigned or its signature does not verify against the
// local keyring (git's gpg.program / gpg.<fmt>.* config). It is the supply-chain
// gate for following a mutable branch: without it, anyone able to push to the
// tracked branch controls what rollops deploys.
func (s *Source) VerifyHeadSignature(ctx context.Context) error {
	if _, err := s.git(ctx, s.dir, "verify-commit", "HEAD"); err != nil {
		return fmt.Errorf("git: HEAD commit signature verification failed: %w", err)
	}
	return nil
}

// requireSignedCommits reports whether commit-signature verification is enabled
// via the ROLLOPS_REQUIRE_SIGNED_COMMITS env var. Default off for backward
// compatibility. Read per-Pull so the setting can be toggled without a restart.
//
// This env gate lives here (rather than being threaded from the daemon through
// the watcher) so the control stays self-contained in the git layer; wiring a
// per-repo config field would additionally require changes in the reconcile
// watcher, which owns Pull's call site.
func requireSignedCommits() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("ROLLOPS_REQUIRE_SIGNED_COMMITS"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// CommitFile writes one file inside the working tree and commits it when it
// changes. It does not push; callers decide when and where writeback is allowed.
func (s *Source) CommitFile(ctx context.Context, relPath string, content []byte, message string) (bool, string, error) {
	clean := filepath.Clean(relPath)
	if clean == "." || strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
		return false, "", fmt.Errorf("git: invalid relative path %q", relPath)
	}
	path := filepath.Join(s.dir, clean)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, "", fmt.Errorf("git: mkdir: %w", err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return false, "", fmt.Errorf("git: write file: %w", err)
	}
	if _, err := s.git(ctx, s.dir, "add", clean); err != nil {
		return false, "", err
	}
	if out, err := s.git(ctx, s.dir, "diff", "--cached", "--quiet"); err == nil {
		return false, "", nil
	} else if out != "" {
		return false, "", err
	}
	if _, err := s.git(ctx, s.dir, "-c", "user.name=rollops", "-c", "user.email=rollops@localhost", "commit", "-m", message); err != nil {
		return false, "", err
	}
	head, err := s.Head(ctx)
	if err != nil {
		return false, "", err
	}
	return true, head, nil
}

// Push publishes the current branch to origin (used by image-automation
// writeback). Auth is threaded through git() as for fetch/clone.
func (s *Source) Push(ctx context.Context) error {
	_, err := s.git(ctx, s.dir, "push", "origin", s.branch)
	return err
}

// CommitFileOnBranch creates (or resets) headBranch off the tracked branch,
// writes one file, and commits it there — the pull-request writeback path,
// which must never touch the tracked branch itself. Returns whether a commit
// was made (false when content matches what the branch already has).
//
// The branch is force-created from the tracked branch each call (checkout -B),
// so a stale head from a previous cycle is refreshed to the current base rather
// than accumulating; the head is deterministic per bump, so re-running updates
// the same proposed change instead of spawning new ones.
func (s *Source) CommitFileOnBranch(ctx context.Context, headBranch, relPath string, content []byte, message string) (bool, error) {
	if headBranch == "" || headBranch == s.branch {
		return false, fmt.Errorf("git: refusing to commit on the tracked branch %q via the PR path", s.branch)
	}
	// A proposal that already carries exactly this content must not be rebuilt.
	// The checkout below re-creates the head branch from the tracked branch, so
	// without this the comparison inside CommitFile runs against a tree that
	// never holds the bump: it always "changes", always commits, and every poll
	// force-pushes an identical diff under a new sha. Each of those pushes
	// cancels the PR's in-flight checks, so the slowest job never reports and
	// the proposal can never merge -- observed on klarlabs-studio/mnemos#267,
	// re-proposed every ~90s for 14 hours.
	//
	// Deliberately not rebased onto the tracked branch while unchanged: staying
	// put may leave the PR behind main, which is visible and fixable, whereas
	// churn is neither.
	if existing, err := s.proposalFile(ctx, headBranch, relPath); err == nil && bytes.Equal(existing, content) {
		return false, nil
	}
	if _, err := s.git(ctx, s.dir, "checkout", "-B", headBranch, s.branch); err != nil {
		return false, fmt.Errorf("git: checkout -B %s: %w", headBranch, err)
	}
	committed, _, err := s.CommitFile(ctx, relPath, content, message)
	if err != nil {
		// Always return to the tracked branch, even on failure, so the working
		// tree is not left on a half-built head for the next reconcile.
		_, _ = s.git(ctx, s.dir, "checkout", s.branch)
		return false, err
	}
	if _, err := s.git(ctx, s.dir, "checkout", s.branch); err != nil {
		return committed, fmt.Errorf("git: return to %s: %w", s.branch, err)
	}
	return committed, nil
}

// proposalFile returns relPath as it stands on an existing proposal branch --
// the local ref if this process created it, otherwise the published one, since
// a restart or a second replica has no local copy. An error means there is no
// such branch or no such file on it, which is simply "no proposal yet".
func (s *Source) proposalFile(ctx context.Context, headBranch, relPath string) ([]byte, error) {
	for _, ref := range []string{headBranch, "origin/" + headBranch} {
		if out, err := s.git(ctx, s.dir, "show", ref+":"+relPath); err == nil {
			return []byte(out), nil
		}
	}
	if _, err := s.git(ctx, s.dir, "fetch", "origin", headBranch); err != nil {
		return nil, err
	}
	out, err := s.git(ctx, s.dir, "show", "FETCH_HEAD:"+relPath)
	if err != nil {
		return nil, err
	}
	return []byte(out), nil
}

// PushBranch force-pushes headBranch to origin. Force is safe and intended
// here: the branch is rollops-owned and deterministically named per bump, so
// pushing refreshes an existing proposal rather than clobbering shared work.
func (s *Source) PushBranch(ctx context.Context, headBranch string) error {
	_, err := s.git(ctx, s.dir, "push", "--force", "origin", headBranch+":"+headBranch)
	return err
}

// git runs a git command, threading per-repo auth via env (GIT_SSH_COMMAND for
// deploy keys; token in the URL is handled by the caller for https).
func (s *Source) git(ctx context.Context, workdir string, args ...string) (string, error) {
	// An https token is passed as a per-command Authorization header so it is
	// never written to disk (no credential helper, no token in the remote URL).
	// Resolve it per command so a rotating provider mints/refreshes on demand.
	token, err := s.auth.token(ctx)
	if err != nil {
		return "", fmt.Errorf("git: resolve token: %w", err)
	}
	if token != "" {
		basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
		args = append([]string{"-c", "http.extraheader=Authorization: Basic " + basic}, args...)
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	if workdir != "" {
		cmd.Dir = workdir
	}
	if s.auth.DeployKeyPath != "" {
		cmd.Env = append(cmd.Environ(),
			"GIT_SSH_COMMAND=ssh -i "+s.auth.DeployKeyPath+" -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new")
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("git %s: %w: %s", redactArgs(args), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// redactArgs renders git args for an error message with any credential (the
// http.extraheader Authorization value) masked, so tokens never reach logs.
func redactArgs(args []string) string {
	cp := make([]string, len(args))
	for i, a := range args {
		if strings.HasPrefix(a, "http.extraheader=") {
			cp[i] = "http.extraheader=Authorization: <redacted>"
		} else {
			cp[i] = a
		}
	}
	return strings.Join(cp, " ")
}
