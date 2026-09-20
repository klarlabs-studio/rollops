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
	data, err := os.ReadFile("../../deploy/rollops.yaml")
	if err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(data)
	if err != nil {
		t.Fatalf("deploy/rollops.yaml does not load: %v", err)
	}
	if c.Spec.Target.Spec["resource"] != "deployment/rollopsd" || c.Spec.Target.Spec["namespace"] != "rollops-system" {
		t.Errorf("target is %v/%v, want rollops-system/deployment/rollopsd",
			c.Spec.Target.Spec["namespace"], c.Spec.Target.Spec["resource"])
	}
	// The manifest path is resolved against the config's own directory, and
	// `..` is refused, so the config must live beside the directory it names.
	mf, _ := c.Spec.Target.Spec["manifestFrom"].(map[string]any)
	if path, _ := mf["path"].(string); path != "kubernetes/rollopsd.yaml" {
		t.Errorf("manifestFrom.path = %q, want kubernetes/rollopsd.yaml", path)
	}
	// The tracked image is what imagePolicy bumps; it must be the daemon's.
	image, _ := c.Spec.Target.Spec["image"].(string)
	if !strings.HasPrefix(image, "ghcr.io/klarlabs-studio/rollopsd:v") {
		t.Errorf("tracked image = %q", image)
	}
	if c.Spec.ImagePolicy == nil {
		t.Fatal("no imagePolicy: the daemon would not follow its own releases")
	}
	// Protected branch: a direct push is rejected and the bump is lost quietly.
	if c.Spec.ImagePolicy.Writeback != "pull-request" {
		t.Errorf("imagePolicy.writeback = %q, want pull-request", c.Spec.ImagePolicy.Writeback)
	}
}
