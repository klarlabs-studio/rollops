// Package depgraph models rollout dependencies as a DAG. Services deploy
// independently by default, or in explicit chains (A completes before B). The
// same graph drives two things: the deploy order (topological layers, so
// independents run in parallel and chains serialize) and the blast-radius
// signal for the risk gate (how many downstream services transitively depend on
// a target).
package depgraph

import (
	"fmt"
	"sort"

	"go.klarlabs.de/rolloffs/internal/rollout"
)

// Graph is a dependency DAG over target refs.
type Graph struct {
	nodes map[string]struct{}
	// edges[from] = set of nodes that depend on `from` (from must finish first).
	dependents map[string]map[string]struct{}
	// prereqs[to] = set of nodes `to` depends on.
	prereqs map[string]map[string]struct{}
}

// New builds a graph from the node set and dependency edges. An edge
// {From, To} means From must complete before To.
func New(nodes []string, deps []rollout.Dependency) *Graph {
	g := &Graph{
		nodes:      make(map[string]struct{}),
		dependents: make(map[string]map[string]struct{}),
		prereqs:    make(map[string]map[string]struct{}),
	}
	for _, n := range nodes {
		g.addNode(n)
	}
	for _, d := range deps {
		g.addNode(d.From)
		g.addNode(d.To)
		g.dependents[d.From][d.To] = struct{}{}
		g.prereqs[d.To][d.From] = struct{}{}
	}
	return g
}

func (g *Graph) addNode(n string) {
	if _, ok := g.nodes[n]; ok {
		return
	}
	g.nodes[n] = struct{}{}
	g.dependents[n] = make(map[string]struct{})
	g.prereqs[n] = make(map[string]struct{})
}

// Layers returns a topological ordering grouped into layers: every node in a
// layer has all its prerequisites in earlier layers, so a layer's nodes can
// deploy in parallel and layers run in sequence. It errors if the graph has a
// cycle.
func (g *Graph) Layers() ([][]string, error) {
	indeg := make(map[string]int, len(g.nodes))
	for n := range g.nodes {
		indeg[n] = len(g.prereqs[n])
	}

	var layers [][]string
	remaining := len(g.nodes)
	for remaining > 0 {
		var layer []string
		for n := range g.nodes {
			if indeg[n] == 0 {
				layer = append(layer, n)
			}
		}
		if len(layer) == 0 {
			return nil, fmt.Errorf("depgraph: cycle detected among %d unresolved nodes", remaining)
		}
		sort.Strings(layer) // deterministic order within a layer
		for _, n := range layer {
			indeg[n] = -1 // mark consumed
			remaining--
			for dep := range g.dependents[n] {
				indeg[dep]--
			}
		}
		layers = append(layers, layer)
	}
	return layers, nil
}

// HasCycle reports whether the graph contains a cycle.
func (g *Graph) HasCycle() bool {
	_, err := g.Layers()
	return err != nil
}

// BlastRadius is the number of services that transitively depend on ref (its
// downstream dependents). This is the count fed to the risk gate.
func (g *Graph) BlastRadius(ref string) int {
	seen := make(map[string]struct{})
	var walk func(string)
	walk = func(n string) {
		for dep := range g.dependents[n] {
			if _, ok := seen[dep]; ok {
				continue
			}
			seen[dep] = struct{}{}
			walk(dep)
		}
	}
	walk(ref)
	return len(seen)
}
