package config

import (
	"fmt"
	"os"
	"sync"

	"gopkg.in/yaml.v3"
)

// Cluster is one entry in the daemon's cluster registry (ROLLOPS_CLUSTERS).
// RolloutSet cluster generators select from this list; kubeconfigs are paths
// to mounted credentials, never stored by Rollops.
type Cluster struct {
	Name       string            `yaml:"name"`
	Kubeconfig string            `yaml:"kubeconfig,omitempty"`
	Context    string            `yaml:"context,omitempty"`
	Labels     map[string]string `yaml:"labels,omitempty"`
}

var (
	registryMu sync.RWMutex
	registry   []Cluster
)

// SetClusterRegistry replaces the in-process registry used by RolloutSet
// cluster expansion. Pass nil or empty to clear. The daemon calls this at boot
// from ROLLOPS_CLUSTERS; tests inject a fixture.
func SetClusterRegistry(clusters []Cluster) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if len(clusters) == 0 {
		registry = nil
		return
	}
	registry = append([]Cluster(nil), clusters...)
}

// ClusterRegistry returns a copy of the current registry.
func ClusterRegistry() []Cluster {
	registryMu.RLock()
	defer registryMu.RUnlock()
	if len(registry) == 0 {
		return nil
	}
	out := make([]Cluster, len(registry))
	copy(out, registry)
	return out
}

// LoadClustersFile reads a cluster registry YAML file. Empty path → nil, nil.
// Duplicate or empty names fail loudly.
func LoadClustersFile(path string) ([]Cluster, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("clusters: read %s: %w", path, err)
	}
	return ParseClusters(data)
}

// ParseClusters decodes a registry document:
//
//	clusters:
//	  - { name: east, kubeconfig: /etc/rollops/east, context: east, labels: { tier: prod } }
func ParseClusters(data []byte) ([]Cluster, error) {
	var doc struct {
		Clusters []Cluster `yaml:"clusters"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("clusters: parse: %w", err)
	}
	if len(doc.Clusters) == 0 {
		return nil, fmt.Errorf("clusters: no clusters listed")
	}
	seen := make(map[string]bool, len(doc.Clusters))
	for i, c := range doc.Clusters {
		if c.Name == "" {
			return nil, fmt.Errorf("clusters: entry %d is missing name", i)
		}
		if seen[c.Name] {
			return nil, fmt.Errorf("clusters: duplicate name %q", c.Name)
		}
		seen[c.Name] = true
	}
	return doc.Clusters, nil
}

// SelectClusters returns registry entries matching matchLabels. An empty
// selector matches every cluster. Every label key must equal.
func SelectClusters(all []Cluster, matchLabels map[string]string) []Cluster {
	if len(matchLabels) == 0 {
		out := make([]Cluster, len(all))
		copy(out, all)
		return out
	}
	var out []Cluster
	for _, c := range all {
		ok := true
		for k, v := range matchLabels {
			if c.Labels[k] != v {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, c)
		}
	}
	return out
}

// LoadClusterRegistryEnv loads ROLLOPS_CLUSTERS into the in-process registry
// when the env var is set. Empty path is a no-op. Used by one-shot CLI and
// plan surfaces so cluster generators expand outside the daemon.
func LoadClusterRegistryEnv(getenv func(string) string) error {
	if getenv == nil {
		getenv = os.Getenv
	}
	path := getenv("ROLLOPS_CLUSTERS")
	if path == "" {
		return nil
	}
	clusters, err := LoadClustersFile(path)
	if err != nil {
		return err
	}
	SetClusterRegistry(clusters)
	return nil
}
