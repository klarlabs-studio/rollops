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
func render(ctx context.Context, spec map[string]any, root string, run cmdRunner) ([]byte, error) {
	out, err := renderSource(ctx, spec, root, run)
	if err != nil {
		return nil, err
	}
	// An optional `image` overrides the container image of matching containers in
	// the rendered manifest — the field image automation patches, so a tracked
	// image can be bumped without editing the embedded manifest by hand.
	if img := str(spec, "image"); img != "" {
		out, err = overrideContainerImage(out, img)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func renderSource(ctx context.Context, spec map[string]any, root string, run cmdRunner) ([]byte, error) {
	// A referenced source (manifestFrom) is exclusive: when present it is the
	// only manifest source, resolved relative to the config-file root. Flat keys
	// (oci/bucket/helm/kustomize/manifest) remain the non-breaking fallback.
	if mf, ok := spec["manifestFrom"].(map[string]any); ok {
		return renderManifestFrom(ctx, mf, root, run)
	}
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
		return renderKustomize(ctx, k, root, run)
	}
	if s, ok := spec["manifest"].(string); ok && s != "" {
		return []byte(s), nil
	}
	return nil, fmt.Errorf("kubernetes: spec must set one of manifestFrom / oci / bucket / helm / kustomize / manifest")
}

// imageRepo strips the :tag (and @digest) from an image ref, leaving the repo.
func imageRepo(image string) string {
	if at := strings.IndexByte(image, '@'); at >= 0 {
		image = image[:at]
	}
	if i := strings.LastIndexByte(image, ':'); i > strings.LastIndexByte(image, '/') {
		image = image[:i]
	}
	return image
}

// overrideContainerImage rewrites, across every document in a (multi-doc)
// manifest, the image of each container whose repository matches the override's
// repository — leaving sidecars on other images untouched. If no container
// matches (single-image workload whose ref differs only by registry), every
// container is set, so the override is never a silent no-op.
func overrideContainerImage(manifest []byte, image string) ([]byte, error) {
	repo := imageRepo(image)
	dec := yaml.NewDecoder(bytes.NewReader(manifest))
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	matchedAny := false
	var docs []*yaml.Node
	for {
		var doc yaml.Node
		if err := dec.Decode(&doc); err != nil {
			break
		}
		setContainerImages(&doc, repo, image, &matchedAny)
		d := doc
		docs = append(docs, &d)
	}
	// Second pass: if nothing matched by repo, set all containers (single-image
	// workloads where only the registry differs).
	if !matchedAny {
		for _, d := range docs {
			setContainerImages(d, "", image, &matchedAny)
		}
	}
	for _, d := range docs {
		if err := enc.Encode(d); err != nil {
			return nil, fmt.Errorf("kubernetes: image override encode: %w", err)
		}
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// setContainerImages walks a document's pod template containers/initContainers
// and sets the image of those whose repo matches repoFilter (empty = all).
func setContainerImages(doc *yaml.Node, repoFilter, image string, matched *bool) {
	tmpl := mappingPath(doc, "spec", "template", "spec")
	if tmpl == nil {
		return
	}
	for _, key := range []string{"containers", "initContainers"} {
		list := childByKey(tmpl, key)
		if list == nil || list.Kind != yaml.SequenceNode {
			continue
		}
		for _, c := range list.Content {
			img := childByKey(c, "image")
			if img == nil {
				continue
			}
			if repoFilter == "" || imageRepo(img.Value) == repoFilter {
				img.Value = image
				*matched = true
			}
		}
	}
}

// mappingPath descends a document's mapping nodes by key, returning the value
// node at the path or nil. It unwraps the document node.
func mappingPath(n *yaml.Node, keys ...string) *yaml.Node {
	cur := n
	if cur.Kind == yaml.DocumentNode && len(cur.Content) > 0 {
		cur = cur.Content[0]
	}
	for _, k := range keys {
		cur = childByKey(cur, k)
		if cur == nil {
			return nil
		}
	}
	return cur
}

// childByKey returns the value node for key in a mapping node, or nil.
func childByKey(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
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
	defer func() { _ = os.RemoveAll(dir) }()

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
	defer func() { _ = os.RemoveAll(dir) }()

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

// renderHelmFrom renders a `manifestFrom.helm` referenced source: a local chart
// dir (resolved against and confined to the config-file root) or a remote chart
// (repo + chart name, passed through), templated with `helm template` and zero
// or more values FILES resolved against the root. Rendering untrusted repo
// content, it uses no --post-renderer and no plugin execution.
func renderHelmFrom(ctx context.Context, h map[string]any, root string, run cmdRunner) ([]byte, error) {
	chart := str(h, "chart")
	if chart == "" {
		return nil, fmt.Errorf("kubernetes: manifestFrom.helm.chart is required")
	}
	release := str(h, "releaseName")
	if release == "" {
		release = "rollops"
	}
	repo := str(h, "repo")
	// A remote chart is a bare name resolved via --repo, or an explicit URL/OCI
	// ref; only a local chart path is rooted and confined.
	chartArg := chart
	if repo == "" && !isRemoteSource(chart) {
		resolved, err := resolveSourcePath(root, chart)
		if err != nil {
			return nil, fmt.Errorf("kubernetes: manifestFrom.helm.chart: %w", err)
		}
		chartArg = resolved
	}
	args := []string{"template", release, chartArg}
	if repo != "" {
		args = append(args, "--repo", repo)
	}
	if version := str(h, "version"); version != "" {
		args = append(args, "--version", version)
	}
	if ns := str(h, "namespace"); ns != "" {
		args = append(args, "--namespace", ns)
	}
	files, err := stringList(h["values"])
	if err != nil {
		return nil, fmt.Errorf("kubernetes: manifestFrom.helm.values: %w", err)
	}
	for _, f := range files {
		vf, err := resolveSourcePath(root, f)
		if err != nil {
			return nil, fmt.Errorf("kubernetes: manifestFrom.helm.values: %w", err)
		}
		args = append(args, "-f", vf)
	}
	out, err := run(ctx, "helm", nil, args...)
	if err != nil {
		return nil, fmt.Errorf("kubernetes: helm template: %w", err)
	}
	return []byte(out), nil
}

// stringList coerces a YAML/JSON list value into a []string.
func stringList(v any) ([]string, error) {
	if v == nil {
		return nil, nil
	}
	raw, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("must be a list of file paths")
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("must be a list of file paths")
		}
		out = append(out, s)
	}
	return out, nil
}

// renderKustomize runs `kubectl kustomize <path>`; path may be a local dir
// (resolved against the config-file root and confined to it) or a remote
// (e.g. a github URL), which passes through untouched.
func renderKustomize(ctx context.Context, k map[string]any, root string, run cmdRunner) ([]byte, error) {
	path := str(k, "path")
	if path == "" {
		return nil, fmt.Errorf("kubernetes: kustomize.path is required")
	}
	return runKustomize(ctx, root, path, run)
}

// runKustomize resolves a kustomize target (remote passthrough, local rooted)
// and runs `kubectl kustomize`. Shared by the flat `kustomize` key and the
// `manifestFrom.kustomize` referenced source.
func runKustomize(ctx context.Context, root, path string, run cmdRunner) ([]byte, error) {
	target, err := resolveSourcePath(root, path)
	if err != nil {
		return nil, fmt.Errorf("kubernetes: kustomize: %w", err)
	}
	out, err := run(ctx, "kubectl", nil, "kustomize", target)
	if err != nil {
		return nil, fmt.Errorf("kubernetes: kustomize build: %w", err)
	}
	return []byte(out), nil
}

// isRemoteSource reports whether a referenced path is a remote source
// (kustomize URL, git ref, OCI ref) that must NOT be rooted or confined —
// kustomize/helm fetch it themselves. Local paths are everything else.
func isRemoteSource(p string) bool {
	if strings.Contains(p, "://") || strings.HasPrefix(p, "git@") {
		return true
	}
	// kustomize's `repo//path` remote marker (e.g. github.com/acme/cfg//overlays).
	if strings.Contains(p, "//") {
		return true
	}
	for _, host := range []string{"github.com/", "gitlab.com/", "bitbucket.org/"} {
		if strings.HasPrefix(p, host) {
			return true
		}
	}
	return false
}

// resolveSourcePath turns a referenced source path into the path handed to the
// render tool. Remote sources pass through. Local sources are confined to the
// config-file root: absolute paths and `..` escapes are rejected (the daemon
// renders untrusted repo content), and the path is joined onto root. When no
// root is threaded (e.g. a remote gRPC apply with no checkout), a local path is
// passed through unchanged to preserve the pre-root behaviour.
func resolveSourcePath(root, path string) (string, error) {
	if isRemoteSource(path) {
		return path, nil
	}
	if root == "" {
		return path, nil // no checkout to root against — legacy CWD-relative behaviour
	}
	clean := filepath.Clean(path)
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the config root", path)
	}
	return filepath.Join(root, clean), nil
}

// renderManifestFrom resolves a referenced manifest source (exactly one of
// path / kustomize / helm) relative to the config-file root. It is exclusive:
// when manifestFrom is set it is the sole manifest source (flat keys ignored).
func renderManifestFrom(ctx context.Context, mf map[string]any, root string, run cmdRunner) ([]byte, error) {
	n := 0
	for _, k := range []string{"path", "kustomize", "helm"} {
		if _, ok := mf[k]; ok {
			n++
		}
	}
	if n != 1 {
		return nil, fmt.Errorf("kubernetes: manifestFrom must set exactly one of path / kustomize / helm")
	}
	if p, ok := mf["path"].(string); ok {
		return renderPath(root, p)
	}
	if k, ok := mf["kustomize"].(string); ok {
		if k == "" {
			return nil, fmt.Errorf("kubernetes: manifestFrom.kustomize must not be empty")
		}
		return runKustomize(ctx, root, k, run)
	}
	if h, ok := mf["helm"].(map[string]any); ok {
		return renderHelmFrom(ctx, h, root, run)
	}
	return nil, fmt.Errorf("kubernetes: manifestFrom must set exactly one of path / kustomize / helm")
}

// renderPath reads a single manifest file referenced relative to the
// config-file root (confined to it).
func renderPath(root, path string) ([]byte, error) {
	if path == "" {
		return nil, fmt.Errorf("kubernetes: manifestFrom.path must not be empty")
	}
	target, err := resolveSourcePath(root, path)
	if err != nil {
		return nil, fmt.Errorf("kubernetes: manifestFrom.path: %w", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		return nil, fmt.Errorf("kubernetes: manifestFrom read %q: %w", path, err)
	}
	return data, nil
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
func manifestFromSpec(ctx context.Context, specJSON []byte, root string, run cmdRunner) ([]byte, error) {
	var spec map[string]any
	if err := json.Unmarshal(specJSON, &spec); err == nil && spec != nil {
		return render(ctx, spec, root, run)
	}
	return specJSON, nil // raw manifest (direct flow / tests)
}

// specReferencesSource reports whether a target spec resolves its manifest from
// a referenced external source (manifestFrom). The engine stamps the drift
// checksum over the RENDERED bytes in that case, so edits to the referenced
// files are detected even under shallow verification.
func specReferencesSource(specJSON []byte) bool {
	var spec map[string]any
	if err := json.Unmarshal(specJSON, &spec); err != nil {
		return false
	}
	_, ok := spec["manifestFrom"].(map[string]any)
	return ok
}
