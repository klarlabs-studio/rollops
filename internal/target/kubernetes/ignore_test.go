package kubernetes

import (
	"context"
	"strings"
	"testing"

	pt "go.klarlabs.de/rollops/pkg/target"
)

func TestEquivalentIgnoring_Replicas(t *testing.T) {
	desired := []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
spec:
  replicas: 1
  template:
    spec:
      containers:
        - name: web
          image: web:v1
`)
	live := []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  resourceVersion: "99"
spec:
  replicas: 5
  template:
    spec:
      containers:
        - name: web
          image: web:v1
status:
  replicas: 5
`)
	same, err := equivalentIgnoring(live, desired, []string{"/spec/replicas"})
	if err != nil {
		t.Fatal(err)
	}
	if !same {
		t.Fatal("ignored replicas + kube noise must not count as drift")
	}
	same, err = equivalentIgnoring(live, desired, nil)
	if err != nil {
		t.Fatal(err)
	}
	if same {
		t.Fatal("without ignoreDifferences, replica drift must still be drift")
	}
}

func TestEquivalentIgnoring_OtherFieldStillDrifts(t *testing.T) {
	desired := []byte("apiVersion: apps/v1\nkind: Deployment\nmetadata: {name: web}\nspec: {replicas: 1, image: web:v1}\n")
	live := []byte("apiVersion: apps/v1\nkind: Deployment\nmetadata: {name: web}\nspec: {replicas: 5, image: web:v2}\n")
	same, err := equivalentIgnoring(live, desired, []string{"spec.replicas"})
	if err != nil {
		t.Fatal(err)
	}
	if same {
		t.Fatal("a non-ignored field (image) must still be drift")
	}
}

func TestDiff_IgnoreDifferences(t *testing.T) {
	desiredYAML := []byte("apiVersion: apps/v1\nkind: Deployment\nmetadata: {name: web}\nspec: {replicas: 1, image: web:v1}\n")
	liveOnlyReplicas := []byte("apiVersion: apps/v1\nkind: Deployment\nmetadata: {name: web}\nspec: {replicas: 5, image: web:v1}\n")
	liveImageToo := []byte("apiVersion: apps/v1\nkind: Deployment\nmetadata: {name: web}\nspec: {replicas: 5, image: web:v2}\n")

	cl := &fakeCluster{liveYAML: liveOnlyReplicas, drift: true}
	tgt := &Target{cl: cl, run: execRunner, ignore: []string{"/spec/replicas"}}
	m := pt.Manifest{Kind: "kubernetes", Spec: []byte(`{"manifest":"x"}`), Rendered: desiredYAML, Checksum: "sum"}

	diff, err := tgt.Diff(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(diff) != "" {
		t.Fatalf("ignored-only diff must be empty, got %q", diff)
	}

	cl.liveYAML = liveImageToo
	diff, err = tgt.Diff(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(diff) == "" {
		t.Fatal("non-ignored field drift must still surface")
	}
}

func TestParseIgnore(t *testing.T) {
	got := parseIgnore(spec{"ignoreDifferences": []any{"/spec/replicas", "spec.template.spec.containers.0.image"}})
	if len(got) != 2 || got[0] != "/spec/replicas" {
		t.Fatalf("parseIgnore = %v", got)
	}
}
