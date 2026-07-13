package kubernetes

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile writes a referenced-source fixture into a temp root.
func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRender_ManifestFrom_Path(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "k8s/api.deployment.yaml", "apiVersion: apps/v1\nkind: Deployment\nmetadata: {name: api}\n")
	spec := map[string]any{"manifestFrom": map[string]any{"path": "k8s/api.deployment.yaml"}}
	out, err := render(context.Background(), spec, root, fakeRunner(&capturedRun{}))
	if err != nil {
		t.Fatalf("render path: %v", err)
	}
	if !strings.Contains(string(out), "name: api") {
		t.Errorf("path source not read: %q", out)
	}
}

func TestRender_ManifestFrom_PathConfinement(t *testing.T) {
	root := t.TempDir()
	for _, bad := range []string{"../secret.yaml", "/etc/passwd", "k8s/../../x.yaml"} {
		spec := map[string]any{"manifestFrom": map[string]any{"path": bad}}
		if _, err := render(context.Background(), spec, root, fakeRunner(&capturedRun{})); err == nil {
			t.Errorf("path %q escaping the root must be rejected", bad)
		}
	}
}

func TestRender_ManifestFrom_KustomizeLocalRooted(t *testing.T) {
	root := t.TempDir()
	c := &capturedRun{out: "kind: Service\n"}
	spec := map[string]any{"manifestFrom": map[string]any{"kustomize": "overlays/prod"}}
	if _, err := render(context.Background(), spec, root, fakeRunner(c)); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "overlays/prod")
	if c.name != "kubectl" || len(c.args) < 2 || c.args[0] != "kustomize" || c.args[1] != want {
		t.Errorf("kustomize cmd = %s %v, want kubectl kustomize %s", c.name, c.args, want)
	}
}

func TestRender_ManifestFrom_KustomizeRemotePassthrough(t *testing.T) {
	root := t.TempDir()
	c := &capturedRun{out: "kind: Service\n"}
	const url = "github.com/acme/cfg//overlays/prod"
	spec := map[string]any{"manifestFrom": map[string]any{"kustomize": url}}
	if _, err := render(context.Background(), spec, root, fakeRunner(c)); err != nil {
		t.Fatal(err)
	}
	if len(c.args) < 2 || c.args[1] != url {
		t.Errorf("remote kustomize must pass through, got %v", c.args)
	}
}

func TestRender_ManifestFrom_ExactlyOneSource(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name string
		mf   map[string]any
	}{
		{"none", map[string]any{}},
		{"two", map[string]any{"path": "a.yaml", "kustomize": "overlays"}},
		{"three", map[string]any{"path": "a.yaml", "kustomize": "overlays", "helm": map[string]any{"chart": "c"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := map[string]any{"manifestFrom": tt.mf}
			if _, err := render(context.Background(), spec, root, fakeRunner(&capturedRun{})); err == nil {
				t.Errorf("manifestFrom %v must require exactly one source", tt.mf)
			}
		})
	}
}

// TestRender_ManifestFrom_PathImageOverride confirms the POST-render image
// override applies to a referenced path source too.
func TestRender_ManifestFrom_PathImageOverride(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "app.yaml", "apiVersion: apps/v1\nkind: Deployment\nmetadata: {name: web}\nspec:\n  template:\n    spec:\n      containers:\n        - name: web\n          image: ghcr.io/acme/web:v1.0.0\n")
	spec := map[string]any{
		"manifestFrom": map[string]any{"path": "app.yaml"},
		"image":        "ghcr.io/acme/web:v2.0.0",
	}
	out, err := render(context.Background(), spec, root, fakeRunner(&capturedRun{}))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(string(out), "ghcr.io/acme/web:v2.0.0") {
		t.Errorf("image override not applied to path source:\n%s", out)
	}
}

// TestRender_ManifestFrom_TakesPrecedence: when both manifestFrom and a flat key
// are present at render time, manifestFrom is the source used.
func TestRender_ManifestFrom_TakesPrecedence(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "app.yaml", "kind: ConfigMap\nmetadata: {name: fromref}\n")
	spec := map[string]any{
		"manifestFrom": map[string]any{"path": "app.yaml"},
		"manifest":     "kind: ConfigMap\nmetadata: {name: inline}\n",
	}
	out, err := render(context.Background(), spec, root, fakeRunner(&capturedRun{}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "fromref") {
		t.Errorf("manifestFrom must take precedence over inline manifest: %q", out)
	}
}

func TestSpecReferencesSource(t *testing.T) {
	if !specReferencesSource([]byte(`{"manifestFrom":{"path":"a.yaml"}}`)) {
		t.Error("manifestFrom spec must be reported as referenced")
	}
	if specReferencesSource([]byte(`{"manifest":"kind: Pod\n"}`)) {
		t.Error("inline manifest must not be referenced")
	}
	if specReferencesSource([]byte(`{"kustomize":{"path":"overlays"}}`)) {
		t.Error("legacy flat kustomize must not be referenced (keeps spec checksum)")
	}
}
