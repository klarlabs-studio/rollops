package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseClusters_Valid(t *testing.T) {
	got, err := ParseClusters([]byte(`
clusters:
  - { name: east, kubeconfig: /etc/east, context: east, labels: { tier: prod, env: prod } }
  - { name: west, kubeconfig: /etc/west, context: west, labels: { tier: prod } }
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "east" || got[0].Labels["tier"] != "prod" {
		t.Fatalf("got %+v", got)
	}
}

func TestParseClusters_Rejects(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want string
	}{
		{"empty", "clusters: []\n", "no clusters"},
		{"missing name", "clusters:\n  - { kubeconfig: /x }\n", "missing name"},
		{"duplicate", "clusters:\n  - { name: a }\n  - { name: a }\n", "duplicate"},
	}
	for _, tc := range cases {
		_, err := ParseClusters([]byte(tc.yaml))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err=%v, want containing %q", tc.name, err, tc.want)
		}
	}
}

func TestLoadClustersFile_EmptyPath(t *testing.T) {
	got, err := LoadClustersFile("")
	if err != nil || got != nil {
		t.Fatalf("empty path: got %v err %v", got, err)
	}
}

func TestLoadClustersFile_Reads(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "clusters.yaml")
	if err := os.WriteFile(p, []byte("clusters:\n  - { name: a, context: a }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadClustersFile(p)
	if err != nil || len(got) != 1 || got[0].Name != "a" {
		t.Fatalf("got %+v err %v", got, err)
	}
}

func TestSelectClusters(t *testing.T) {
	all := []Cluster{
		{Name: "east", Labels: map[string]string{"tier": "prod", "env": "prod"}},
		{Name: "west", Labels: map[string]string{"tier": "prod", "env": "staging"}},
		{Name: "dev", Labels: map[string]string{"tier": "dev"}},
	}
	if got := SelectClusters(all, nil); len(got) != 3 {
		t.Fatalf("empty selector: %d", len(got))
	}
	got := SelectClusters(all, map[string]string{"tier": "prod"})
	if len(got) != 2 || got[0].Name != "east" || got[1].Name != "west" {
		t.Fatalf("tier=prod: %+v", got)
	}
	got = SelectClusters(all, map[string]string{"tier": "prod", "env": "prod"})
	if len(got) != 1 || got[0].Name != "east" {
		t.Fatalf("tier+env: %+v", got)
	}
	if got := SelectClusters(all, map[string]string{"tier": "none"}); len(got) != 0 {
		t.Fatalf("no match: %+v", got)
	}
}

func TestSetClusterRegistry(t *testing.T) {
	t.Cleanup(func() { SetClusterRegistry(nil) })
	SetClusterRegistry([]Cluster{{Name: "x"}})
	if len(ClusterRegistry()) != 1 {
		t.Fatal("registry not set")
	}
	SetClusterRegistry(nil)
	if ClusterRegistry() != nil {
		t.Fatal("registry not cleared")
	}
}
