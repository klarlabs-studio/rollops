package main

import (
	"os"
	"path/filepath"
	"testing"

	"go.klarlabs.de/rollops/internal/config"
)

func TestLoadClustersFromEnvShape(t *testing.T) {
	// Mirrors the daemon boot path: LoadClustersFile + SetClusterRegistry.
	t.Cleanup(func() { config.SetClusterRegistry(nil) })
	dir := t.TempDir()
	p := filepath.Join(dir, "clusters.yaml")
	if err := os.WriteFile(p, []byte(`
clusters:
  - { name: east, kubeconfig: /e, context: east, labels: { tier: prod } }
  - { name: west, kubeconfig: /w, context: west, labels: { tier: prod } }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	clusters, err := config.LoadClustersFile(p)
	if err != nil {
		t.Fatal(err)
	}
	config.SetClusterRegistry(clusters)
	if n := len(config.ClusterRegistry()); n != 2 {
		t.Fatalf("registry len = %d", n)
	}
}
