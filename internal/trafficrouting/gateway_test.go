package trafficrouting

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"go.klarlabs.de/rollops/internal/config"
)

// fakeKubectl records calls and serves a canned HTTPRoute for `get`.
type fakeKubectl struct {
	routeJSON string
	patchBody string
	getArgs   []string
	patchArgs []string
}

func (f *fakeKubectl) run(_ context.Context, _ []byte, args ...string) (string, error) {
	switch args[0] {
	case "get":
		f.getArgs = args
		return f.routeJSON, nil
	case "patch":
		f.patchArgs = args
		// capture the -p body (last arg after --type=json -p)
		for i, a := range args {
			if a == "-p" && i+1 < len(args) {
				f.patchBody = args[i+1]
			}
		}
		return "", nil
	}
	return "", nil
}

const routeTwoRefs = `{"spec":{"rules":[{"backendRefs":[{"name":"web-stable"},{"name":"web-canary"}]}]}}`

func TestGatewayRouter_SetWeight_PatchesByName(t *testing.T) {
	fk := &fakeKubectl{routeJSON: routeTwoRefs}
	r := &gatewayRouter{run: fk.run}
	err := r.SetWeight(context.Background(), Change{
		Route: "web", Namespace: "prod",
		StableService: "web-stable", CanaryService: "web-canary", Weight: 30,
	})
	if err != nil {
		t.Fatalf("SetWeight: %v", err)
	}
	var ops []patchOp
	if err := json.Unmarshal([]byte(fk.patchBody), &ops); err != nil {
		t.Fatalf("patch body not JSON: %v (%s)", err, fk.patchBody)
	}
	// stable ref index 0 → 70, canary ref index 1 → 30.
	want := map[string]int{
		"/spec/rules/0/backendRefs/0/weight": 70,
		"/spec/rules/0/backendRefs/1/weight": 30,
	}
	got := map[string]int{}
	for _, o := range ops {
		if o.Op != "add" {
			t.Errorf("op = %q, want add", o.Op)
		}
		got[o.Path] = o.Value
	}
	for path, w := range want {
		if got[path] != w {
			t.Errorf("%s = %d, want %d (ops=%s)", path, got[path], w, fk.patchBody)
		}
	}
}

func TestGatewayRouter_SetWeight_NoMatchingBackend(t *testing.T) {
	fk := &fakeKubectl{routeJSON: `{"spec":{"rules":[{"backendRefs":[{"name":"other"}]}]}}`}
	r := &gatewayRouter{run: fk.run}
	err := r.SetWeight(context.Background(), Change{
		Route: "web", Namespace: "prod", StableService: "web-stable", CanaryService: "web-canary", Weight: 50,
	})
	if err == nil || !strings.Contains(err.Error(), "no backendRefs named") {
		t.Fatalf("err = %v, want no-matching-backend error", err)
	}
	if fk.patchArgs != nil {
		t.Error("must not patch when no backendRef matches")
	}
}

func TestBuildRouter_GatewayProviderNoPlugin(t *testing.T) {
	r, err := BuildRouter(&config.TrafficRouting{Provider: "gateway", Route: "web", StableService: "s", CanaryService: "c"})
	if err != nil {
		t.Fatalf("BuildRouter(gateway): %v", err)
	}
	if _, ok := r.(*gatewayRouter); !ok {
		t.Errorf("want *gatewayRouter, got %T", r)
	}
}

func TestBuildRouter_UnknownProvider(t *testing.T) {
	if _, err := BuildRouter(&config.TrafficRouting{Provider: "nope"}); err == nil {
		t.Fatal("unknown provider must error")
	}
}
