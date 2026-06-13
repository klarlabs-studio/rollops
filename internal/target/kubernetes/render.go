package kubernetes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// cmdRunner runs an external command with optional stdin and returns stdout.
// Injectable so rendering is testable without real helm/kubectl.
type cmdRunner func(ctx context.Context, name string, stdin []byte, args ...string) (string, error)

func execRunner(ctx context.Context, name string, stdin []byte, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("%s: %w: %s", name, err, errb.String())
	}
	return out.String(), nil
}

// render produces the deployable Kubernetes manifest from the target spec. Like
// Flux and Argo, Rollops renders Helm charts and Kustomize overlays — not just
// raw manifests — so existing chart/overlay users can adopt it unchanged.
//
// Precedence: oci > helm > kustomize > manifest > (raw spec, used by the direct
// flow). The Git source is the daemon's own desired-state poll; oci adds the
// Flux OCIRepository model — pull a manifest/kustomize bundle from an OCI
// registry instead of a checkout.
func render(ctx context.Context, spec map[string]any, run cmdRunner) ([]byte, error) {
	if o, ok := spec["oci"].(map[string]any); ok {
		return renderOCI(ctx, o, run)
	}
	if b, ok := spec["bucket"].(map[string]any); ok {
		return renderBucket(ctx, b, run)
	}
	if h, ok := spec["helm"].(map[string]any); ok {
		return renderHelm(ctx, h, run)
	}
	if k, ok := spec["kustomize"].(map[string]any); ok {
		return renderKustomize(ctx, k, run)
	}
	if s, ok := spec["manifest"].(string); ok && s != "" {
		return []byte(s), nil
	}
	return nil, fmt.Errorf("kubernetes: spec must set one of oci / bucket / helm / kustomize / manifest")
}

// renderBucket syncs a desired-state tree from an object-storage bucket (the
// Flux Bucket source) to a temp dir, then renders it like the oci source. The
// bucket URL scheme selects the CLI: s3:// uses `aws s3 sync`, gs:// uses
// `gsutil -m rsync -r`. Credentials are the CLI's ambient resolution.
func renderBucket(ctx context.Context, b map[string]any, run cmdRunner) ([]byte, error) {
	urlStr := str(b, "url")
	if urlStr == "" {
		return nil, fmt.Errorf("kubernetes: bucket.url is required")
	}
	dir, err := os.MkdirTemp("", "rollops-bucket-")
	if err != nil {
		return nil, fmt.Errorf("kubernetes: bucket temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	switch {
	case strings.HasPrefix(urlStr, "s3://"):
		if _, err := run(ctx, "aws", nil, "s3", "sync", urlStr, dir); err != nil {
			return nil, fmt.Errorf("kubernetes: aws s3 sync %q: %w", urlStr, err)
		}
	case strings.HasPrefix(urlStr, "gs://"):
		if _, err := run(ctx, "gsutil", nil, "-m", "rsync", "-r", urlStr, dir); err != nil {
			return nil, fmt.Errorf("kubernetes: gsutil rsync %q: %w", urlStr, err)
		}
	default:
		return nil, fmt.Errorf("kubernetes: bucket.url must be an s3:// or gs:// URL: %q", urlStr)
	}

	root := dir
	if sub := str(b, "path"); sub != "" {
		root = filepath.Join(dir, sub)
	}
	return renderTree(ctx, root, str(b, "render"), str(b, "file"), "bucket", run)
}

// renderOCI pulls a non-Helm OCI artifact (a bundle of Kubernetes manifests or a
// kustomize tree, the Flux OCIRepository model) with `oras pull`, then renders
// its contents. `path` is an optional subdirectory within the artifact;
// `render` selects how the extracted contents become a manifest (kustomize by
// default, or a single manifest file via `file`).
func renderOCI(ctx context.Context, o map[string]any, run cmdRunner) ([]byte, error) {
	ref := str(o, "ref")
	if ref == "" {
		return nil, fmt.Errorf("kubernetes: oci.ref is required")
	}
	dir, err := os.MkdirTemp("", "rollops-oci-")
	if err != nil {
		return nil, fmt.Errorf("kubernetes: oci temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	if _, err := run(ctx, "oras", nil, "pull", ref, "-o", dir); err != nil {
		return nil, fmt.Errorf("kubernetes: oras pull %q: %w", ref, err)
	}
	root := dir
	if sub := str(o, "path"); sub != "" {
		root = filepath.Join(dir, sub)
	}
	return renderTree(ctx, root, str(o, "render"), str(o, "file"), "oci", run)
}

// renderTree renders a fetched desired-state directory: a single manifest file
// (mode "manifest") or a kustomize build of the tree (default). Shared by the
// oci and bucket sources.
func renderTree(ctx context.Context, root, mode, file, src string, run cmdRunner) ([]byte, error) {
	if mode == "manifest" {
		if file == "" {
			return nil, fmt.Errorf("kubernetes: %s.file is required when render is manifest", src)
		}
		data, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			return nil, fmt.Errorf("kubernetes: %s read manifest: %w", src, err)
		}
		return data, nil
	}
	out, err := run(ctx, "kubectl", nil, "kustomize", root)
	if err != nil {
		return nil, fmt.Errorf("kubernetes: %s kustomize build: %w", src, err)
	}
	return []byte(out), nil
}

// renderHelm runs `helm template`, passing inline values over stdin. A remote
// chart (repo + chart name) avoids needing a local checkout path.
func renderHelm(ctx context.Context, h map[string]any, run cmdRunner) ([]byte, error) {
	chart := str(h, "chart")
	if chart == "" {
		return nil, fmt.Errorf("kubernetes: helm.chart is required")
	}
	release := str(h, "releaseName")
	if release == "" {
		release = "rollops"
	}
	args := []string{"template", release, chart}
	if repo := str(h, "repo"); repo != "" {
		args = append(args, "--repo", repo)
	}
	if version := str(h, "version"); version != "" {
		args = append(args, "--version", version)
	}
	if ns := str(h, "namespace"); ns != "" {
		args = append(args, "--namespace", ns)
	}
	var stdin []byte
	if vals, ok := h["values"].(map[string]any); ok && len(vals) > 0 {
		y, err := yaml.Marshal(vals)
		if err != nil {
			return nil, fmt.Errorf("kubernetes: marshal helm values: %w", err)
		}
		stdin = y
		args = append(args, "-f", "/dev/stdin")
	}
	out, err := run(ctx, "helm", stdin, args...)
	if err != nil {
		return nil, fmt.Errorf("kubernetes: helm template: %w", err)
	}
	return []byte(out), nil
}

// renderKustomize runs `kubectl kustomize <path>`; path may be a local dir or a
// remote (e.g. a github URL), so a checkout is optional.
func renderKustomize(ctx context.Context, k map[string]any, run cmdRunner) ([]byte, error) {
	path := str(k, "path")
	if path == "" {
		return nil, fmt.Errorf("kubernetes: kustomize.path is required")
	}
	out, err := run(ctx, "kubectl", nil, "kustomize", path)
	if err != nil {
		return nil, fmt.Errorf("kubernetes: kustomize build: %w", err)
	}
	return []byte(out), nil
}

func str(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// manifestFromSpec resolves the deployable manifest for a target manifest's
// spec, supporting the config flow (JSON spec with helm/kustomize/manifest) and
// the direct flow (raw manifest bytes).
func manifestFromSpec(ctx context.Context, specJSON []byte, run cmdRunner) ([]byte, error) {
	var spec map[string]any
	if err := json.Unmarshal(specJSON, &spec); err == nil && spec != nil {
		return render(ctx, spec, run)
	}
	return specJSON, nil // raw manifest (direct flow / tests)
}
