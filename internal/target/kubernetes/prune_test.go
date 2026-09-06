package kubernetes

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestLabelValue(t *testing.T) {
	cases := map[string]string{
		"shop/prod/web":          "shop-prod-web",
		"Payments/Prod/API":      "payments-prod-api",
		"":                       "rollops",
		strings.Repeat("x", 100): strings.Repeat("x", 63),
	}
	for in, want := range cases {
		if got := labelValue(in); got != want {
			t.Errorf("labelValue(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLabelManifest_InjectsLabelEveryDoc(t *testing.T) {
	manifest := []byte(`apiVersion: v1
kind: Service
metadata:
  name: svc
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  labels:
    app: web
`)
	out, err := labelManifest(manifest, "shop-web")
	if err != nil {
		t.Fatal(err)
	}

	dec := yaml.NewDecoder(bytes.NewReader(out))
	n := 0
	for {
		var doc map[string]any
		if err := dec.Decode(&doc); err != nil {
			break
		}
		if doc == nil {
			continue
		}
		n++
		meta := doc["metadata"].(map[string]any)
		labels := meta["labels"].(map[string]any)
		if labels[PruneLabel] != "shop-web" {
			t.Errorf("doc %d missing prune label: %v", n, labels)
		}
	}
	if n != 2 {
		t.Errorf("expected 2 docs, labeled %d", n)
	}
	// Existing labels are preserved.
	if !strings.Contains(string(out), "app: web") {
		t.Error("existing labels should be preserved")
	}
}

// #158: the identity label and the destructive behaviour are separate
// decisions. Labelling only when prune is on means a `prune: false` target's
// resources carry nothing tying them to it — so when its RolloutConfig is
// deleted (#154) nothing, including a future reaper, can even enumerate what
// it left behind.
func TestLabelManifest_LeavesSelectorAndPodTemplateAlone(t *testing.T) {
	const deploy = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
spec:
  selector:
    matchLabels:
      app: api
  template:
    metadata:
      labels:
        app: api
    spec:
      containers:
        - name: api
          image: api:1
`
	out, err := labelManifest([]byte(deploy), "demo-prod-app")
	if err != nil {
		t.Fatalf("labelManifest: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(out, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	meta := doc["metadata"].(map[string]any)
	labels := meta["labels"].(map[string]any)
	if labels[PruneLabel] != "demo-prod-app" {
		t.Errorf("top-level metadata.labels missing the target label: %v", labels)
	}

	spec := doc["spec"].(map[string]any)

	// spec.selector is IMMUTABLE on a Deployment. Adding a key here would make
	// every subsequent apply fail on an existing workload.
	sel := spec["selector"].(map[string]any)["matchLabels"].(map[string]any)
	if _, found := sel[PruneLabel]; found {
		t.Errorf("label leaked into the immutable spec.selector: %v", sel)
	}

	// The pod template's labels feed the selector and changing them rolls every
	// pod. Adopting a running workload must not restart it.
	tmplLabels := spec["template"].(map[string]any)["metadata"].(map[string]any)["labels"].(map[string]any)
	if _, found := tmplLabels[PruneLabel]; found {
		t.Errorf("label leaked into spec.template.metadata.labels, which would roll the pods: %v", tmplLabels)
	}
}

// The label is unconditional; the destructive flags are not. #158.
func TestPruneArgs_OnlyWhenAsked(t *testing.T) {
	if got := pruneArgs(false, "demo-prod-app"); got != nil {
		t.Errorf("prune: false produced flags %v — a target that opted out of deletion must never get --prune", got)
	}
	got := pruneArgs(true, "demo-prod-app")
	want := []string{"--prune", "--selector", PruneLabel + "=demo-prod-app"}
	if len(got) != len(want) {
		t.Fatalf("pruneArgs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("pruneArgs = %v, want %v", got, want)
		}
	}
}

// The selector is the only thing between "this target's resources" and "the
// namespace". #154.
func TestReapArgs_AlwaysScopedToTheTargetMarker(t *testing.T) {
	got := reapArgs(nil, "demo-prod-app")
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "--selector "+PruneLabel+"=demo-prod-app") {
		t.Fatalf("reap is not scoped to the target marker: %v", got)
	}
	if got[0] != "delete" {
		t.Errorf("first arg = %q, want delete", got[0])
	}
	// Unset types must fall back to the documented default rather than an empty
	// type list, which kubectl would reject or interpret unpredictably.
	if !strings.Contains(joined, "all") {
		t.Errorf("no resource types in %v", got)
	}
	if !strings.Contains(joined, "--ignore-not-found") {
		t.Errorf("a retried reap should not error on an empty match: %v", got)
	}
}

// Widening is possible but must be explicit, and must not drop the selector.
func TestReapArgs_HonoursExplicitTypes(t *testing.T) {
	got := reapArgs([]string{"deployment", "service", "ingress"}, "x-y")
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "deployment,service,ingress") {
		t.Errorf("explicit types not passed through: %v", got)
	}
	if !strings.Contains(joined, "--selector "+PruneLabel+"=x-y") {
		t.Errorf("explicit types dropped the selector — that would delete every object of these kinds: %v", got)
	}
}

// A target that did not opt in must be refused even if something calls it.
func TestReapTarget_RefusesWithoutOptIn(t *testing.T) {
	k := &kubectlCluster{pruneVal: "demo", reapOnDelete: false}
	if _, err := k.ReapTarget(context.Background()); err == nil {
		t.Fatal("reaped a target that never opted in")
	}
}

// An empty marker would make the selector match everything in the namespace.
func TestReapTarget_RefusesEmptyMarker(t *testing.T) {
	k := &kubectlCluster{pruneVal: "", reapOnDelete: true}
	if _, err := k.ReapTarget(context.Background()); err == nil {
		t.Fatal("reaped with an empty marker — the selector would match the namespace")
	}
}
