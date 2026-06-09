package kubernetes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"

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
// Flux and Argo, Rolloffs renders Helm charts and Kustomize overlays — not just
// raw manifests — so existing chart/overlay users can adopt it unchanged.
//
// Precedence: helm > kustomize > manifest > (raw spec, used by the direct flow).
func render(ctx context.Context, spec map[string]any, run cmdRunner) ([]byte, error) {
	if h, ok := spec["helm"].(map[string]any); ok {
		return renderHelm(ctx, h, run)
	}
	if k, ok := spec["kustomize"].(map[string]any); ok {
		return renderKustomize(ctx, k, run)
	}
	if s, ok := spec["manifest"].(string); ok && s != "" {
		return []byte(s), nil
	}
	return nil, fmt.Errorf("kubernetes: spec must set one of helm / kustomize / manifest")
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
		release = "rolloffs"
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
