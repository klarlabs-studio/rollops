package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const rolloutSetYAML = `
apiVersion: rollops.klarlabs.de/v1
kind: RolloutSet
metadata: { name: web }
generators:
  - list:
      elements:
        - { name: east, kubeconfig: /etc/rollops/east, context: east }
        - { name: west, kubeconfig: /etc/rollops/west, context: west }
template:
  spec:
    target:
      kind: kubernetes
      ref: "web@{{name}}"
      criticality: low
      spec:
        kubeconfig: "{{kubeconfig}}"
        context: "{{context}}"
        namespace: web
        resource: deployment/web
        manifest: |
          apiVersion: apps/v1
          kind: Deployment
          metadata: {name: web}
          spec: {replicas: 1}
    strategy: { type: rolling }
`

func TestExpandRolloutSet_ListTwoElements(t *testing.T) {
	got, err := expandRolloutSet([]byte(rolloutSetYAML), "web.yaml")
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d configs, want 2", len(got))
	}
	refs := map[string]bool{}
	for _, nc := range got {
		if nc.Path != "web.yaml" {
			t.Errorf("path = %q, want the source file (Git holds the template)", nc.Path)
		}
		if nc.Config.Kind != Kind {
			t.Errorf("kind = %q, want RolloutConfig", nc.Config.Kind)
		}
		ref := nc.Config.Spec.Target.Ref
		if refs[ref] {
			t.Errorf("duplicate ref %q", ref)
		}
		refs[ref] = true
		if nc.Config.Spec.Target.Kind != "kubernetes" {
			t.Errorf("target.kind = %q", nc.Config.Spec.Target.Kind)
		}
	}
	if !refs["web@east"] || !refs["web@west"] {
		t.Errorf("refs = %v, want web@east and web@west", refs)
	}
	for _, nc := range got {
		kc, _ := nc.Config.Spec.Target.Spec["kubeconfig"].(string)
		ctx, _ := nc.Config.Spec.Target.Spec["context"].(string)
		switch nc.Config.Spec.Target.Ref {
		case "web@east":
			if kc != "/etc/rollops/east" || ctx != "east" {
				t.Errorf("east kubeconfig/context = %q %q", kc, ctx)
			}
		case "web@west":
			if kc != "/etc/rollops/west" || ctx != "west" {
				t.Errorf("west kubeconfig/context = %q %q", kc, ctx)
			}
		}
	}
}

func TestExpandRolloutSet_ClusterSelector(t *testing.T) {
	t.Cleanup(func() { SetClusterRegistry(nil) })
	SetClusterRegistry([]Cluster{
		{Name: "east", Kubeconfig: "/etc/east", Context: "east", Labels: map[string]string{"tier": "prod", "env": "prod"}},
		{Name: "west", Kubeconfig: "/etc/west", Context: "west", Labels: map[string]string{"tier": "prod", "env": "staging"}},
		{Name: "dev", Kubeconfig: "/etc/dev", Context: "dev", Labels: map[string]string{"tier": "dev"}},
	})
	const src = `
apiVersion: rollops.klarlabs.de/v1
kind: RolloutSet
metadata: { name: web }
generators:
  - cluster:
      selector:
        matchLabels: { tier: prod }
template:
  spec:
    target:
      kind: kubernetes
      ref: "web@{{cluster.name}}"
      criticality: low
      env: "{{cluster.labels.env}}"
      spec:
        kubeconfig: "{{cluster.kubeconfig}}"
        context: "{{cluster.context}}"
        namespace: web
        resource: deployment/web
        manifest: |
          apiVersion: apps/v1
          kind: Deployment
          metadata: {name: web}
          spec: {replicas: 1}
    strategy: { type: rolling }
`
	got, err := expandRolloutSet([]byte(src), "web.yaml")
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d, want 2 prod clusters", len(got))
	}
	refs := map[string]*Config{}
	for _, nc := range got {
		refs[nc.Config.Spec.Target.Ref] = nc.Config
	}
	if refs["web@east"] == nil || refs["web@west"] == nil {
		t.Fatalf("refs = %v", refs)
	}
	if refs["web@east"].Spec.Target.Env != "prod" {
		t.Errorf("east env = %q", refs["web@east"].Spec.Target.Env)
	}
	kc, _ := refs["web@east"].Spec.Target.Spec["kubeconfig"].(string)
	if kc != "/etc/east" {
		t.Errorf("east kubeconfig = %q", kc)
	}
}

func TestExpandRolloutSet_ClusterNeedsRegistry(t *testing.T) {
	t.Cleanup(func() { SetClusterRegistry(nil) })
	SetClusterRegistry(nil)
	const src = `
apiVersion: rollops.klarlabs.de/v1
kind: RolloutSet
metadata: { name: web }
generators:
  - cluster: {}
template:
  spec:
    target:
      kind: fake
      ref: "web@{{name}}"
      criticality: low
      spec: { x: 1 }
    strategy: { type: rolling }
`
	_, err := expandRolloutSet([]byte(src), "web.yaml")
	if err == nil || !strings.Contains(err.Error(), "ROLLOPS_CLUSTERS") {
		t.Fatalf("want registry error, got %v", err)
	}
}

func TestExpandRolloutSet_RejectsBothGenerators(t *testing.T) {
	src := strings.Replace(rolloutSetYAML, "- list:", "- cluster:\n      selector: { matchLabels: { tier: prod } }\n  - list:", 1)
	_, err := expandRolloutSet([]byte(src), "web.yaml")
	if err == nil || (!strings.Contains(err.Error(), "exactly one") && !strings.Contains(err.Error(), "not both")) {
		t.Fatalf("want single-generator error, got %v", err)
	}
}

func TestExpandRolloutSet_UnknownPlaceholder(t *testing.T) {
	src := strings.Replace(rolloutSetYAML, "{{context}}", "{{nope}}", 1)
	_, err := expandRolloutSet([]byte(src), "web.yaml")
	if err == nil || !strings.Contains(err.Error(), "{{nope}}") {
		t.Fatalf("want unknown placeholder error, got %v", err)
	}
}

func TestExpandRolloutSet_DuplicateRefs(t *testing.T) {
	src := strings.Replace(rolloutSetYAML, "name: west", "name: east", 1)
	_, err := expandRolloutSet([]byte(src), "web.yaml")
	if err == nil || !strings.Contains(err.Error(), "duplicate target.ref") {
		t.Fatalf("want duplicate ref error, got %v", err)
	}
}

func TestLoadAllFromDir_ExpandsRolloutSet(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "web.yaml"), []byte(rolloutSetYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadAllFromDir(dir, RepoRef{Path: "web.yaml"})
	if err != nil {
		t.Fatalf("LoadAllFromDir: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d, want 2 expanded configs", len(got))
	}
}

func TestLoadAllFromDir_SetAmongConfigs(t *testing.T) {
	dir := t.TempDir()
	apps := filepath.Join(dir, "apps")
	if err := os.MkdirAll(apps, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(apps, "web.yaml"), []byte(rolloutSetYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(apps, "solo.yaml"), []byte(validYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadAllFromDir(dir, RepoRef{Path: "apps"})
	if err != nil {
		t.Fatalf("LoadAllFromDir: %v", err)
	}
	if len(got) != 3 { // 2 from the set + 1 solo
		t.Fatalf("got %d configs, want 3", len(got))
	}
}

func TestLoadDocuments_ExpandsAndRefuseApply(t *testing.T) {
	got, err := LoadDocuments([]byte(rolloutSetYAML), "web.yaml")
	if err != nil {
		t.Fatalf("LoadDocuments: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d", len(got))
	}
	if err := RefuseApplyRolloutSet([]byte(rolloutSetYAML)); err == nil {
		t.Fatal("expected refuse apply of RolloutSet")
	}
	solo := []byte(`apiVersion: rollops.klarlabs.de/v1
kind: RolloutConfig
metadata: { name: solo }
spec:
  target:
    kind: fake
    ref: solo
    criticality: low
    spec: { x: 1 }
  strategy: { type: rolling }
`)
	if err := RefuseApplyRolloutSet(solo); err != nil {
		t.Fatalf("ordinary config: %v", err)
	}
}
