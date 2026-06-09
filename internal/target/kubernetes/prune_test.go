package kubernetes

import (
	"bytes"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestLabelValue(t *testing.T) {
	cases := map[string]string{
		"shop/prod/web":          "shop-prod-web",
		"Payments/Prod/API":      "payments-prod-api",
		"":                       "rolloffs",
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
	out, err := labelManifest(manifest, PruneLabel, "shop-web")
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
