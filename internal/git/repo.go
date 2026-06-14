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
}

// Clone checks out url@branch into dir. Each watched repo gets its own Source —
// isolation is a property of Git structure.
func Clone(ctx context.Context, url, branch, dir string, auth Auth) (*Source, error) {
	s := &Source{dir: dir, branch: branch, auth: auth}
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
	return before != after, after, nil
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
