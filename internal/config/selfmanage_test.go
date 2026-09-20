package config_test

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"go.klarlabs.de/rollops/internal/config"
)

// rollops deploys rollops: rollops.yaml at the repo root is the config the daemon loads
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
	// the daemon looked for the manifest at the root and refused the whole batch.
	const manifest = "deploy/kubernetes/rollopsd-deployment.yaml"
	mf, _ := c.Spec.Target.Spec["manifestFrom"].(map[string]any)
	if path, _ := mf["path"].(string); path != manifest {
		t.Errorf("manifestFrom.path = %q, want %s", path, manifest)
	}
	if _, err := os.Stat("../../" + manifest); err != nil {
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

// The self-managed manifest must hold the Deployment and nothing else.
//
// The daemon applies it with its own ServiceAccount, which cannot get
// cert-manager ClusterIssuers and must never be able to rewrite the ClusterRole
// that grants it everything else. When this manifest was the full install file,
// the API server refused the server-side dry run and the preflight applied
// nothing, three reconciles running:
//
//	clusterissuers.cert-manager.io "rollopsd-selfsigned" is forbidden: User
//	"system:serviceaccount:rollops-system:rollopsd" cannot get resource
//	"clusterissuers" in API group "cert-manager.io" at the cluster scope
//
// Bootstrap objects belong in rollopsd-infra.yaml, applied by a human once.
func TestSelfManagedManifestIsTheDeploymentAlone(t *testing.T) {
	data, err := os.ReadFile("../../deploy/kubernetes/rollopsd-deployment.yaml")
	if err != nil {
		t.Fatal(err)
	}

	var kinds []string
	dec := yaml.NewDecoder(bytes.NewReader(data))
	for {
		var doc struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Name      string `yaml:"name"`
				Namespace string `yaml:"namespace"`
			} `yaml:"metadata"`
		}
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("manifest does not parse: %v", err)
		}
		if doc.Kind == "" {
			continue // an empty document between separators
		}
		kinds = append(kinds, doc.Kind)
		if doc.Kind == "Deployment" && (doc.Metadata.Name != "rollopsd" || doc.Metadata.Namespace != "rollops-system") {
			t.Errorf("Deployment is %s/%s, want rollops-system/rollopsd",
				doc.Metadata.Namespace, doc.Metadata.Name)
		}
	}
	if len(kinds) != 1 || kinds[0] != "Deployment" {
		t.Errorf("self-managed manifest holds %v; want exactly [Deployment] — "+
			"the daemon has no permission to apply its own RBAC or TLS material", kinds)
	}
}

// Bootstrap is the other half: whatever the daemon may not apply must still be
// somewhere a human can apply, or a fresh install has no namespace to land in.
func TestBootstrapManifestHoldsWhatTheDaemonCannotApply(t *testing.T) {
	data, err := os.ReadFile("../../deploy/kubernetes/rollopsd-infra.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{
		"Namespace", "ClusterIssuer", "Issuer", "Certificate",
		"ServiceAccount", "ClusterRole", "ClusterRoleBinding",
		"PersistentVolumeClaim", "Service",
	} {
		if !strings.Contains(string(data), "kind: "+kind+"\n") {
			t.Errorf("rollopsd-infra.yaml has no %s; a clean install would not come up", kind)
		}
	}
	if strings.Contains(string(data), "kind: Deployment\n") {
		t.Error("rollopsd-infra.yaml also defines the Deployment; applying bootstrap would " +
			"clobber whatever the daemon rolled out")
	}
}
