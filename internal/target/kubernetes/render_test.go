package kubernetes

import (
	"context"
	"os"
	"path/filepath"
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

// ociRunner simulates `oras pull -o <dir>` by writing files into the output dir,
// then handles the follow-up kustomize call.
func ociRunner(t *testing.T, files map[string]string, kustomizeOut string) (cmdRunner, *capturedRun) {
	t.Helper()
	c := &capturedRun{}
	run := func(_ context.Context, name string, _ []byte, args ...string) (string, error) {
		if name == "oras" && len(args) > 0 && args[0] == "pull" {
			// Find -o <dir> and write the artifact's files there.
			dir := ""
			for i, a := range args {
				if a == "-o" && i+1 < len(args) {
					dir = args[i+1]
				}
			}
			for rel, content := range files {
				p := filepath.Join(dir, rel)
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					return "", err
				}
				if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
					return "", err
				}
			}
			return "", nil
		}
		c.name, c.args = name, args // capture the kustomize call
		return kustomizeOut, nil
	}
	return run, c
}

func TestRender_OCI_ManifestMode(t *testing.T) {
	run, _ := ociRunner(t, map[string]string{"deploy/app.yaml": "kind: Deployment\nmetadata:\n  name: app\n"}, "")
	spec := map[string]any{"oci": map[string]any{
		"ref": "oci://ghcr.io/acme/app:v1", "path": "deploy", "render": "manifest", "file": "app.yaml",
	}}
	out, err := render(context.Background(), spec, run)
	if err != nil {
		t.Fatalf("render oci manifest: %v", err)
	}
	if !strings.Contains(string(out), "name: app") {
		t.Errorf("manifest from artifact = %q", out)
	}
}

func TestRender_OCI_KustomizeMode(t *testing.T) {
	run, c := ociRunner(t, map[string]string{"kustomization.yaml": "resources: [app.yaml]\n"}, "kind: Service\n")
	spec := map[string]any{"oci": map[string]any{"ref": "oci://ghcr.io/acme/app:v1"}}
	out, err := render(context.Background(), spec, run)
	if err != nil {
		t.Fatalf("render oci kustomize: %v", err)
	}
	if c.name != "kubectl" || c.args[0] != "kustomize" {
		t.Errorf("expected kubectl kustomize, got %s %v", c.name, c.args)
	}
	if !strings.Contains(string(out), "Service") {
		t.Errorf("kustomized artifact = %q", out)
	}
}

func TestRender_OCI_RequiresRef(t *testing.T) {
	if _, err := render(context.Background(), map[string]any{"oci": map[string]any{}}, fakeRunner(&capturedRun{})); err == nil {
		t.Error("oci without ref must error")
	}
}

// bucketRunner simulates `aws s3 sync` / `gsutil rsync` (last arg is the dest
// dir) by writing files there, then handles the kustomize call.
func bucketRunner(t *testing.T, syncCmd string, files map[string]string, kustomizeOut string) (cmdRunner, *capturedRun) {
	t.Helper()
	c := &capturedRun{}
	run := func(_ context.Context, name string, _ []byte, args ...string) (string, error) {
		if name == syncCmd {
			dir := args[len(args)-1]
			for rel, content := range files {
				p := filepath.Join(dir, rel)
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					return "", err
				}
				if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
					return "", err
				}
			}
			return "", nil
		}
		c.name, c.args = name, args
		return kustomizeOut, nil
	}
	return run, c
}

func TestRender_Bucket_S3Kustomize(t *testing.T) {
	run, c := bucketRunner(t, "aws", map[string]string{"kustomization.yaml": "resources: [app.yaml]\n"}, "kind: Service\n")
	spec := map[string]any{"bucket": map[string]any{"url": "s3://acme-cfg/prod"}}
	out, err := render(context.Background(), spec, run)
	if err != nil {
		t.Fatalf("render bucket s3: %v", err)
	}
	if c.name != "kubectl" || c.args[0] != "kustomize" {
		t.Errorf("expected kubectl kustomize, got %s %v", c.name, c.args)
	}
	if !strings.Contains(string(out), "Service") {
		t.Errorf("out = %q", out)
	}
}

func TestRender_Bucket_GCSManifest(t *testing.T) {
	run, _ := bucketRunner(t, "gsutil", map[string]string{"deploy/app.yaml": "kind: Deployment\n"}, "")
	spec := map[string]any{"bucket": map[string]any{
		"url": "gs://acme-cfg/prod", "path": "deploy", "render": "manifest", "file": "app.yaml",
	}}
	out, err := render(context.Background(), spec, run)
	if err != nil {
		t.Fatalf("render bucket gcs: %v", err)
	}
	if !strings.Contains(string(out), "Deployment") {
		t.Errorf("out = %q", out)
	}
}

func TestRender_ImageOverride(t *testing.T) {
	manifest := `apiVersion: apps/v1
kind: Deployment
metadata: {name: web}
spec:
  template:
    spec:
      containers:
        - name: web
          image: ghcr.io/acme/web:v1.0.0
        - name: sidecar
          image: ghcr.io/acme/proxy:v2.0.0
`
	spec := map[string]any{"manifest": manifest, "image": "ghcr.io/acme/web:v1.3.0"}
	out, err := render(context.Background(), spec, fakeRunner(&capturedRun{}))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, "ghcr.io/acme/web:v1.3.0") {
		t.Errorf("web image not overridden:\n%s", s)
	}
	if !strings.Contains(s, "ghcr.io/acme/proxy:v2.0.0") {
		t.Errorf("sidecar (different repo) must be untouched:\n%s", s)
	}
}

func TestRender_ImageOverride_NoRepoMatchSetsAll(t *testing.T) {
	// Single-image workload where only the registry differs → still applied.
	manifest := "apiVersion: apps/v1\nkind: Deployment\nmetadata: {name: web}\nspec:\n  template:\n    spec:\n      containers:\n        - name: web\n          image: old.registry/acme/web:v1\n"
	spec := map[string]any{"manifest": manifest, "image": "ghcr.io/acme/web:v2"}
	out, err := render(context.Background(), spec, fakeRunner(&capturedRun{}))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(string(out), "ghcr.io/acme/web:v2") {
		t.Errorf("override should apply when no repo matches:\n%s", out)
	}
}

func TestRender_Bucket_RequiresURL(t *testing.T) {
	if _, err := render(context.Background(), map[string]any{"bucket": map[string]any{}}, fakeRunner(&capturedRun{})); err == nil {
		t.Error("bucket without url must error")
	}
}

func TestRender_Bucket_UnsupportedScheme(t *testing.T) {
	spec := map[string]any{"bucket": map[string]any{"url": "ftp://nope/x"}}
	if _, err := render(context.Background(), spec, fakeRunner(&capturedRun{})); err == nil {
		t.Error("non-s3/gs bucket url must error")
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
