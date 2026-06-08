package depgraph

import (
	"reflect"
	"testing"

	"go.klarlabs.de/rolloffs/internal/rollout"
)

func dep(from, to string) rollout.Dependency { return rollout.Dependency{From: from, To: to} }

func TestLayers_IndependentInOneLayer(t *testing.T) {
	g := New([]string{"a", "b", "c"}, nil)
	layers, err := g.Layers()
	if err != nil {
		t.Fatal(err)
	}
	if len(layers) != 1 || len(layers[0]) != 3 {
		t.Fatalf("independents should be one parallel layer, got %v", layers)
	}
}

func TestLayers_ChainSerializes(t *testing.T) {
	// a -> b -> c
	g := New([]string{"a", "b", "c"}, []rollout.Dependency{dep("a", "b"), dep("b", "c")})
	layers, err := g.Layers()
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"a"}, {"b"}, {"c"}}
	if !reflect.DeepEqual(layers, want) {
		t.Fatalf("layers = %v, want %v", layers, want)
	}
}

func TestLayers_DiamondParallelizesWherePossible(t *testing.T) {
	// a -> b, a -> c, b -> d, c -> d
	g := New(nil, []rollout.Dependency{dep("a", "b"), dep("a", "c"), dep("b", "d"), dep("c", "d")})
	layers, err := g.Layers()
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"a"}, {"b", "c"}, {"d"}}
	if !reflect.DeepEqual(layers, want) {
		t.Fatalf("layers = %v, want %v", layers, want)
	}
}

func TestCycle_Detected(t *testing.T) {
	g := New(nil, []rollout.Dependency{dep("a", "b"), dep("b", "c"), dep("c", "a")})
	if !g.HasCycle() {
		t.Fatal("expected cycle to be detected")
	}
	if _, err := g.Layers(); err == nil {
		t.Fatal("Layers must error on a cycle")
	}
}

func TestBlastRadius_TransitiveDependents(t *testing.T) {
	// a -> b -> c, a -> d
	g := New(nil, []rollout.Dependency{dep("a", "b"), dep("b", "c"), dep("a", "d")})
	if got := g.BlastRadius("a"); got != 3 { // b, c, d
		t.Errorf("blast radius of a = %d, want 3", got)
	}
	if got := g.BlastRadius("b"); got != 1 { // c
		t.Errorf("blast radius of b = %d, want 1", got)
	}
	if got := g.BlastRadius("c"); got != 0 {
		t.Errorf("blast radius of leaf c = %d, want 0", got)
	}
}
