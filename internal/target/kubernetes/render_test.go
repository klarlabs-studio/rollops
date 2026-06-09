package kubernetes

import (
	"context"
	"strings"
	"testing"
)

type capturedRun struct {
	name  string
	args  []string
	stdin string
	out   string
}

func fakeRunner(c *capturedRun) cmdRunner {
	return func(_ context.Context, name string, stdin []byte, args ...string) (string, error) {
		c.name = name
		c.args = args
		c.stdin = string(stdin)
		return c.out, nil
	}
}

func TestRender_Helm(t *testing.T) {
	c := &capturedRun{out: "apiVersion: v1\nkind: Service\n"}
	spec := map[string]any{
		"helm": map[string]any{
			"chart":       "nginx",
			"repo":        "https://charts.bitnami.com/bitnami",
			"version":     "15.0.0",
			"releaseName": "web",
			"values":      map[string]any{"replicaCount": 3},
		},
	}
	out, err := render(context.Background(), spec, fakeRunner(c))
	if err != nil {
		t.Fatal(err)
	}
	if c.name != "helm" {
		t.Errorf("ran %q, want helm", c.name)
	}
	joined := strings.Join(c.args, " ")
	for _, want := range []string{"template", "web", "nginx", "--repo", "--version 15.0.0", "-f /dev/stdin"} {
		if !strings.Contains(joined, want) {
			t.Errorf("helm args missing %q: %v", want, c.args)
		}
	}
	if !strings.Contains(c.stdin, "replicaCount: 3") {
		t.Errorf("values not passed via stdin: %q", c.stdin)
	}
	if !strings.Contains(string(out), "kind: Service") {
		t.Errorf("rendered output = %q", out)
	}
}

func TestRender_Kustomize(t *testing.T) {
	c := &capturedRun{out: "kind: Deployment\n"}
	spec := map[string]any{"kustomize": map[string]any{"path": "github.com/acme/cfg//overlays/prod"}}
	out, err := render(context.Background(), spec, fakeRunner(c))
	if err != nil {
		t.Fatal(err)
	}
	if c.name != "kubectl" || c.args[0] != "kustomize" || c.args[1] != "github.com/acme/cfg//overlays/prod" {
		t.Errorf("kustomize cmd = %s %v", c.name, c.args)
	}
	if !strings.Contains(string(out), "Deployment") {
		t.Errorf("out = %q", out)
	}
}

func TestRender_RawManifest(t *testing.T) {
	out, err := render(context.Background(), map[string]any{"manifest": "kind: Pod\n"}, fakeRunner(&capturedRun{}))
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "kind: Pod\n" {
		t.Errorf("out = %q", out)
	}
}

func TestRender_RequiresOneSource(t *testing.T) {
	if _, err := render(context.Background(), map[string]any{"namespace": "x"}, fakeRunner(&capturedRun{})); err == nil {
		t.Error("spec with no helm/kustomize/manifest should error")
	}
}

func TestManifestFromSpec_RawFallback(t *testing.T) {
	// Direct flow: m.Spec is raw YAML, not a JSON object.
	raw := []byte("apiVersion: apps/v1\nkind: Deployment\n")
	out, err := manifestFromSpec(context.Background(), raw, fakeRunner(&capturedRun{}))
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(raw) {
		t.Errorf("raw manifest not passed through: %q", out)
	}
}
