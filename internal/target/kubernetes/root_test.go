package kubernetes

import (
	"context"
	"path/filepath"
	"testing"
)

// TestRender_Kustomize_LocalPathRooted proves the crux of the referenced-source
// feature: a LOCAL kustomize path resolves against the config-file root (not the
// daemon CWD), so `kubectl kustomize` runs against <root>/<sub>.
func TestRender_Kustomize_LocalPathRooted(t *testing.T) {
	root := t.TempDir()
	c := &capturedRun{out: "kind: Deployment\n"}
	spec := map[string]any{"kustomize": map[string]any{"path": "overlays/prod"}}
	if _, err := render(context.Background(), spec, root, fakeRunner(c)); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "overlays/prod")
	if c.name != "kubectl" || len(c.args) < 2 || c.args[0] != "kustomize" || c.args[1] != want {
		t.Errorf("kustomize cmd = %s %v, want kubectl kustomize %s", c.name, c.args, want)
	}
}

// TestRender_Kustomize_RemotePassthrough confirms a remote kustomize URL is
// never rooted or confined — kustomize fetches it itself.
func TestRender_Kustomize_RemotePassthrough(t *testing.T) {
	root := t.TempDir()
	c := &capturedRun{out: "kind: Service\n"}
	const url = "github.com/acme/cfg//overlays/prod"
	spec := map[string]any{"kustomize": map[string]any{"path": url}}
	if _, err := render(context.Background(), spec, root, fakeRunner(c)); err != nil {
		t.Fatal(err)
	}
	if len(c.args) < 2 || c.args[1] != url {
		t.Errorf("remote kustomize must pass through unchanged, got %v", c.args)
	}
}

// TestRender_Kustomize_NoRootLegacy: with no root threaded (e.g. a remote gRPC
// apply with no checkout), a local path falls back to the pre-root CWD-relative
// behaviour rather than erroring.
func TestRender_Kustomize_NoRootLegacy(t *testing.T) {
	c := &capturedRun{out: "kind: Deployment\n"}
	spec := map[string]any{"kustomize": map[string]any{"path": "overlays/prod"}}
	if _, err := render(context.Background(), spec, "", fakeRunner(c)); err != nil {
		t.Fatal(err)
	}
	if len(c.args) < 2 || c.args[1] != "overlays/prod" {
		t.Errorf("no-root local path should pass through, got %v", c.args)
	}
}

func TestResolveSourcePath_Confinement(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name    string
		path    string
		wantErr bool
		want    string
	}{
		{"simple", "k8s/app.yaml", false, filepath.Join(root, "k8s/app.yaml")},
		{"dot-slash", "./k8s/app.yaml", false, filepath.Join(root, "k8s/app.yaml")},
		{"parent-escape", "../secrets/app.yaml", true, ""},
		{"nested-escape", "k8s/../../etc/passwd", true, ""},
		{"absolute", "/etc/passwd", true, ""},
		{"remote-url", "https://example.com/x.yaml", false, "https://example.com/x.yaml"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveSourcePath(root, tt.path)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("resolveSourcePath(%q) must error", tt.path)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveSourcePath(%q): %v", tt.path, err)
			}
			if got != tt.want {
				t.Errorf("resolveSourcePath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

// TestManifestFromSpec_RootThreaded proves Root travels from the manifest into
// the rooted render: a spec with a local kustomize path renders against Root.
func TestManifestFromSpec_RootThreaded(t *testing.T) {
	root := t.TempDir()
	c := &capturedRun{out: "kind: Deployment\n"}
	specJSON := []byte(`{"kustomize":{"path":"overlays/prod"}}`)
	if _, err := manifestFromSpec(context.Background(), specJSON, root, fakeRunner(c)); err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "overlays/prod"); len(c.args) < 2 || c.args[1] != want {
		t.Errorf("manifestFromSpec did not root the path: %v", c.args)
	}
}
