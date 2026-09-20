package config_test

import (
	"os"
	"strings"
	"testing"

	"go.klarlabs.de/rollops/internal/config"
)

// rollops deploys rollops: deploy/rollops.yaml is the config the daemon loads
// for itself. A typo here is not caught by any other test — the daemon simply
// stops updating itself, quietly, which is exactly how it fell four releases
// behind the image its own repository pinned.
func TestSelfManagementConfigIsValid(t *testing.T) {
	data, err := os.ReadFile("../../rollops.yaml")
	if err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(data)
	if err != nil {
		t.Fatalf("rollops.yaml does not load: %v", err)
	}
	if c.Spec.Target.Spec["resource"] != "deployment/rollopsd" || c.Spec.Target.Spec["namespace"] != "rollops-system" {
		t.Errorf("target is %v/%v, want rollops-system/deployment/rollopsd",
			c.Spec.Target.Spec["namespace"], c.Spec.Target.Spec["resource"])
	}
	// The daemon resolves a referenced manifest against the repo checkout root
	// and the CLI against the config file's own directory, and `..` is refused.
	// A config at the repo root is the only place those two agree — in deploy/
	// the daemon looked for deploy/kubernetes/rollopsd.yaml at the root and
	// refused the whole batch.
	mf, _ := c.Spec.Target.Spec["manifestFrom"].(map[string]any)
	if path, _ := mf["path"].(string); path != "deploy/kubernetes/rollopsd.yaml" {
		t.Errorf("manifestFrom.path = %q, want deploy/kubernetes/rollopsd.yaml", path)
	}
	if _, err := os.Stat("../../" + "deploy/kubernetes/rollopsd.yaml"); err != nil {
		t.Errorf("the referenced manifest is not where the config says: %v", err)
	}
	// The tracked image is what imagePolicy bumps; it must be the daemon's.
	image, _ := c.Spec.Target.Spec["image"].(string)
	if !strings.HasPrefix(image, "ghcr.io/klarlabs-studio/rollopsd:v") {
		t.Errorf("tracked image = %q", image)
	}
	// The daemon applies this from inside the cluster, where kubeconfig
	// contexts do not exist: a `context` here refuses every reconcile.
	if ctxName, ok := c.Spec.Target.Spec["context"]; ok {
		t.Errorf("self-config pins kubeconfig context %v; the in-cluster daemon has none", ctxName)
	}
	if c.Spec.ImagePolicy == nil {
		t.Fatal("no imagePolicy: the daemon would not follow its own releases")
	}
	// Protected branch: a direct push is rejected and the bump is lost quietly.
	if c.Spec.ImagePolicy.Writeback != "pull-request" {
		t.Errorf("imagePolicy.writeback = %q, want pull-request", c.Spec.ImagePolicy.Writeback)
	}
}
