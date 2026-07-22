package reconcile

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
	// minor mode → highest within major 1 = v1.2.0 (not v2.0.0), and all semver
	// modes now pin the selected tag by digest (fakeTags.Digest → sha256:deadbeef).
	if ref != "ghcr.io/acme/web:v1.2.0@sha256:deadbeef" {
		t.Errorf("ref = %q, want ghcr.io/acme/web:v1.2.0@sha256:deadbeef", ref)
	}
	if got, _ := bumped.Spec.Target.Spec["image"].(string); got != "ghcr.io/acme/web:v1.2.0@sha256:deadbeef" {
		t.Errorf("bumped config image = %q", got)
	}
	// The file on disk was rewritten + committed, digest-pinned.
	data, _ := os.ReadFile(filepath.Join(src.Dir(), "apps/web.yaml"))
	if !strings.Contains(string(data), "ghcr.io/acme/web:v1.2.0@sha256:deadbeef") {
		t.Errorf("committed file not bumped+pinned:\n%s", data)
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
	src := newGitRepo(t, cfgYAML)
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
	// Migration keeps the pin: the matched semver tag's digest IS the pinned one.
	if ref != "ghcr.io/acme/web:v1.2.0@sha256:abc123" {
		t.Errorf("ref = %q, want ghcr.io/acme/web:v1.2.0@sha256:abc123", ref)
	}
	if got, _ := bumped.Spec.Target.Spec["image"].(string); got != "ghcr.io/acme/web:v1.2.0@sha256:abc123" {
		t.Errorf("migrated image = %q", got)
	}
}

func TestImageAuto_EmbeddedSemverPinPreserved(t *testing.T) {
	// SECURITY (finding #1): a semver policy on a digest-pinned ref
	// (repo:v1.1.0@sha256:GOOD) must NEVER be rewritten to the unpinned
	// repo:v1.1.0 — that would re-enable tag-mutation attacks. With no newer tag
	// available the ref is left untouched (still pinned).
	cfgYAML := strings.ReplaceAll(imgConfigYAML, "ghcr.io/acme/web:v1.0.0", "ghcr.io/acme/web:v1.1.0@sha256:abc123")
	src := newGitRepo(t, cfgYAML)
	cfg, _ := config.Load([]byte(cfgYAML))
	// Only the current version exists (as a semver tag) → no newer tag → no-op.
	reg := fakeRegistry{tags: []string{"v1.1.0"}, digests: map[string]string{"v1.1.0": "sha256:abc123"}}
	bumped, ref, err := ImageAuto{Scanner: reg}.Process(context.Background(), src, config.NamedConfig{Path: "apps/web.yaml", Config: cfg})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if ref != "" {
		t.Errorf("ref = %q, want no-op (pin must not be stripped)", ref)
	}
	if got, _ := bumped.Spec.Target.Spec["image"].(string); got != "ghcr.io/acme/web:v1.1.0@sha256:abc123" {
		t.Errorf("image must stay digest-pinned, got %q", got)
	}
	data, _ := os.ReadFile(filepath.Join(src.Dir(), "apps/web.yaml"))
	if !strings.Contains(string(data), "ghcr.io/acme/web:v1.1.0@sha256:abc123") {
		t.Errorf("committed file lost its pin:\n%s", data)
	}
}

func TestImageAuto_EmbeddedSemverPinAdvancesPinned(t *testing.T) {
	// A digest-pinned semver ref DOES advance to a newer qualifying tag — but the
	// new ref is itself digest-pinned (never downgraded to a mutable tag).
	cfgYAML := strings.ReplaceAll(imgConfigYAML, "ghcr.io/acme/web:v1.0.0", "ghcr.io/acme/web:v1.1.0@sha256:old")
	src := newGitRepo(t, cfgYAML)
	cfg, _ := config.Load([]byte(cfgYAML))
	reg := fakeRegistry{
		tags:    []string{"v1.1.0", "v1.2.0"},
		digests: map[string]string{"v1.1.0": "sha256:old", "v1.2.0": "sha256:new"},
	}
	_, ref, err := ImageAuto{Scanner: reg}.Process(context.Background(), src, config.NamedConfig{Path: "apps/web.yaml", Config: cfg})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if ref != "ghcr.io/acme/web:v1.2.0@sha256:new" {
		t.Errorf("ref = %q, want ghcr.io/acme/web:v1.2.0@sha256:new", ref)
	}
}

func TestImageAuto_SemverPinFailClosed(t *testing.T) {
	// SECURITY (finding #1, fail-closed): when the current ref is digest-pinned
	// but the selected tag's digest cannot be resolved, the update is skipped with
	// an error rather than emitting a mutable (unpinned) ref.
	cfgYAML := strings.ReplaceAll(imgConfigYAML, "ghcr.io/acme/web:v1.0.0", "ghcr.io/acme/web:v1.1.0@sha256:old")
	src := newGitRepo(t, cfgYAML)
	cfg, _ := config.Load([]byte(cfgYAML))
	// v1.2.0 is offered as a newer tag, but its digest resolves to "" (unknown).
	reg := fakeRegistry{tags: []string{"v1.1.0", "v1.2.0"}, digests: map[string]string{"v1.1.0": "sha256:old"}}
	_, ref, err := ImageAuto{Scanner: reg}.Process(context.Background(), src, config.NamedConfig{Path: "apps/web.yaml", Config: cfg})
	if err == nil {
		t.Fatal("expected fail-closed error when a pinned ref's new digest is unresolvable")
	}
	if !strings.Contains(err.Error(), "refusing to unpin") {
		t.Errorf("err = %v, want a refusing-to-unpin error", err)
	}
	if ref != "" {
		t.Errorf("ref = %q, want empty on fail-closed", ref)
	}
}

func TestImageAuto_RegistryAllowlist(t *testing.T) {
	// SECURITY (finding #7): image automation is refused when the image's registry
	// is not in the configured allowlist.
	cfgYAML := strings.Replace(imgConfigYAML, "  imagePolicy:\n    mode: minor\n",
		"  imagePolicy:\n    mode: minor\n    allowedRegistries: [docker.io]\n", 1)
	src := newGitRepo(t, cfgYAML)
	cfg, err := config.Load([]byte(cfgYAML))
	if err != nil {
		t.Fatal(err)
	}
	_, ref, err := ImageAuto{Scanner: fakeTags{"v1.1.0"}}.Process(context.Background(), src, config.NamedConfig{Path: "apps/web.yaml", Config: cfg})
	if err == nil || !strings.Contains(err.Error(), "allowedRegistries") {
		t.Fatalf("expected allowlist rejection for ghcr.io, got ref=%q err=%v", ref, err)
	}
}

func TestImageAuto_RegistryAllowlistPermits(t *testing.T) {
	// The same policy allows ghcr.io → the bump proceeds (digest-pinned).
	cfgYAML := strings.Replace(imgConfigYAML, "  imagePolicy:\n    mode: minor\n",
		"  imagePolicy:\n    mode: minor\n    allowedRegistries: [ghcr.io]\n", 1)
	src := newGitRepo(t, cfgYAML)
	cfg, err := config.Load([]byte(cfgYAML))
	if err != nil {
		t.Fatal(err)
	}
	_, ref, err := ImageAuto{Scanner: fakeTags{"v1.0.0", "v1.1.0"}}.Process(context.Background(), src, config.NamedConfig{Path: "apps/web.yaml", Config: cfg})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if ref != "ghcr.io/acme/web:v1.1.0@sha256:deadbeef" {
		t.Errorf("ref = %q, want ghcr.io/acme/web:v1.1.0@sha256:deadbeef", ref)
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

// TestImageAuto_PullRequestWriteback proves the protected-branch path: a bump in
// pull-request mode opens a PR and does NOT deploy. The cluster must never lead
// Git — the deploy waits for the merge — so Process returns ref="" and the
// tracked branch is left untouched, while a PR is opened via the API.
func TestImageAuto_PullRequestWriteback(t *testing.T) {
	// Stub GitHub: record the create-PR call, succeed on auto-merge.
	var opened bool
	var head, base string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/pulls"):
			opened = true
			var b map[string]string
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &b)
			head, base = b["head"], b["base"]
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"node_id":"n","html_url":"https://github.com/acme/web/pull/1"}`))
		case r.URL.Path == "/graphql":
			_, _ = w.Write([]byte(`{"data":{}}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	cfgYAML := strings.Replace(imgConfigYAML, "image: ghcr.io/acme/web:v1.0.0", "image: ghcr.io/acme/web:latest", 1)
	cfgYAML = strings.Replace(cfgYAML, "mode: minor", "mode: digest\n    allowMutableTags: true\n    writeback: pull-request", 1)

	src := newGitRepo(t, cfgYAML).WithURL("https://github.com/acme/web").WithAPIBase(srv.URL)
	cfg, err := config.Load([]byte(cfgYAML))
	if err != nil {
		t.Fatal(err)
	}
	bumped, ref, err := ImageAuto{Scanner: fakeDigest("sha256:brandnew")}.
		Process(context.Background(), src, config.NamedConfig{Path: "apps/web.yaml", Config: cfg})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}

	// No deploy this cycle: ref empty, config returned unchanged.
	if ref != "" {
		t.Fatalf("PR mode must not deploy; got ref=%q", ref)
	}
	if got, _ := bumped.Spec.Target.Spec["image"].(string); got != "ghcr.io/acme/web:latest" {
		t.Errorf("returned config must be the original (un-bumped), got %q", got)
	}
	// A PR was opened into the tracked branch.
	if !opened {
		t.Fatal("no create-PR call reached the API")
	}
	if head != "rollops/image/web" || base != "main" {
		t.Fatalf("wrong PR head/base: %q -> %q", head, base)
	}
	// The tracked branch on disk was NOT modified — the bump lives only on the
	// PR branch. (checkout -B head, commit there, checkout back to main.)
	data, _ := os.ReadFile(filepath.Join(src.Dir(), "apps/web.yaml"))
	if strings.Contains(string(data), "sha256:brandnew") {
		t.Errorf("tracked branch must be untouched in PR mode:\n%s", data)
	}
	// The PR branch was pushed to origin with the bump.
	if out := gitOut(t, src.Dir(), "ls-remote", "origin", "rollops/image/web"); !strings.Contains(out, "rollops/image/web") {
		t.Fatalf("PR branch was not pushed to origin: %q", out)
	}
	head2 := gitOut(t, src.Dir(), "show", "origin/rollops/image/web:apps/web.yaml")
	if !strings.Contains(head2, "sha256:brandnew") {
		t.Errorf("PR branch does not carry the bump:\n%s", head2)
	}
}

// TestImageAuto_PushWritebackUnchanged pins that the default (push) mode still
// commits to the tracked branch and deploys, so this feature is additive.
func TestImageAuto_PushWritebackUnchanged(t *testing.T) {
	cfgYAML := strings.Replace(imgConfigYAML, "image: ghcr.io/acme/web:v1.0.0", "image: ghcr.io/acme/web:latest", 1)
	cfgYAML = strings.Replace(cfgYAML, "mode: minor", "mode: digest\n    allowMutableTags: true", 1) // no writeback → default push
	src := newGitRepo(t, cfgYAML)
	cfg, _ := config.Load([]byte(cfgYAML))
	_, ref, err := ImageAuto{Scanner: fakeDigest("sha256:pushed")}.
		Process(context.Background(), src, config.NamedConfig{Path: "apps/web.yaml", Config: cfg})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if ref == "" {
		t.Fatal("push mode must deploy this cycle (non-empty ref)")
	}
	data, _ := os.ReadFile(filepath.Join(src.Dir(), "apps/web.yaml"))
	if !strings.Contains(string(data), "sha256:pushed") {
		t.Errorf("push mode must bump the tracked branch:\n%s", data)
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return string(out)
}
