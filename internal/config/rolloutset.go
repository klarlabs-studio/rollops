package config

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// KindRolloutSet is a load-time generator: a list or cluster generator plus a
// template expands in memory into ordinary RolloutConfigs. Git holds the
// template; generated configs are ephemeral. Matrix/git generators are out of
// scope.
const KindRolloutSet = "RolloutSet"

type rolloutSetDoc struct {
	APIVersion string                `yaml:"apiVersion"`
	Kind       string                `yaml:"kind"`
	Metadata   Metadata              `yaml:"metadata"`
	Generators []rolloutSetGenerator `yaml:"generators"`
	Template   map[string]any        `yaml:"template"`
}

type rolloutSetGenerator struct {
	List    *listGenerator    `yaml:"list,omitempty"`
	Cluster *clusterGenerator `yaml:"cluster,omitempty"`
}

type listGenerator struct {
	Elements []map[string]any `yaml:"elements"`
}

type clusterGenerator struct {
	Selector *clusterSelector `yaml:"selector,omitempty"`
}

type clusterSelector struct {
	MatchLabels map[string]string `yaml:"matchLabels,omitempty"`
}

func peekKind(data []byte) (string, error) {
	var head struct {
		APIVersion string `yaml:"apiVersion"`
		Kind       string `yaml:"kind"`
	}
	if err := yaml.Unmarshal(data, &head); err != nil {
		return "", fmt.Errorf("parse: %w", err)
	}
	if head.APIVersion != "" && head.APIVersion != SchemaVersion {
		return "", fmt.Errorf("unsupported apiVersion %q (this build supports %q)", head.APIVersion, SchemaVersion)
	}
	return head.Kind, nil
}

// LoadDocuments loads one YAML file as either a RolloutConfig or a RolloutSet
// (expanded in memory into N ordinary configs). Path is used only for errors
// and NamedConfig.Path.
func LoadDocuments(data []byte, path string) ([]NamedConfig, error) {
	return loadDocuments(data, path)
}

// KindOf returns the document kind after the apiVersion gate. An empty kind is
// treated as RolloutConfig by LoadDocuments.
func KindOf(data []byte) (string, error) {
	return peekKind(data)
}

// ErrApplyRolloutSet is returned when a caller tries to apply a RolloutSet
// directly. Sets expand at watch load; plan/doctor preview members, reconcile
// applies each generated target.
var ErrApplyRolloutSet = fmt.Errorf("config: refuse apply of kind %s — plan/doctor expand it; the daemon watcher applies each generated target", KindRolloutSet)

// RefuseApplyRolloutSet returns ErrApplyRolloutSet when data is a RolloutSet.
func RefuseApplyRolloutSet(data []byte) error {
	kind, err := KindOf(data)
	if err != nil {
		return err
	}
	if kind == KindRolloutSet {
		return ErrApplyRolloutSet
	}
	return nil
}

func loadDocuments(data []byte, path string) ([]NamedConfig, error) {
	kind, err := peekKind(data)
	if err != nil {
		return nil, fmt.Errorf("config: %s: %w", path, err)
	}
	switch kind {
	case Kind, "":
		c, err := Load(data)
		if err != nil {
			return nil, fmt.Errorf("config: %s: %w", path, err)
		}
		return []NamedConfig{{Path: path, Config: c}}, nil
	case KindRolloutSet:
		return expandRolloutSet(data, path)
	default:
		return nil, fmt.Errorf("config: %s: unsupported kind %q (want %q or %q)", path, kind, Kind, KindRolloutSet)
	}
}

func expandRolloutSet(data []byte, path string) ([]NamedConfig, error) {
	var set rolloutSetDoc
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&set); err != nil {
		return nil, fmt.Errorf("config: %s: parse RolloutSet: %w", path, err)
	}
	if set.APIVersion != SchemaVersion {
		return nil, fmt.Errorf("config: %s: unsupported apiVersion %q (this build supports %q)", path, set.APIVersion, SchemaVersion)
	}
	if set.Metadata.Name == "" {
		return nil, fmt.Errorf("config: %s: RolloutSet metadata.name is required", path)
	}
	if set.Template == nil {
		return nil, fmt.Errorf("config: %s: RolloutSet template is required", path)
	}
	if len(set.Generators) != 1 {
		return nil, fmt.Errorf("config: %s: RolloutSet requires exactly one generator (list or cluster)", path)
	}
	g := set.Generators[0]
	switch {
	case g.List != nil && g.Cluster != nil:
		return nil, fmt.Errorf("config: %s: generator must be list or cluster, not both", path)
	case g.List != nil:
		return expandFromElements(set, path, g.List.Elements)
	case g.Cluster != nil:
		return expandFromClusters(set, path, g.Cluster)
	default:
		return nil, fmt.Errorf("config: %s: RolloutSet generator must be list or cluster", path)
	}
}

func expandFromClusters(set rolloutSetDoc, path string, gen *clusterGenerator) ([]NamedConfig, error) {
	all := ClusterRegistry()
	if len(all) == 0 {
		return nil, fmt.Errorf("config: %s: cluster generator needs a registry (set ROLLOPS_CLUSTERS)", path)
	}
	var match map[string]string
	if gen.Selector != nil {
		match = gen.Selector.MatchLabels
	}
	selected := SelectClusters(all, match)
	if len(selected) == 0 {
		return nil, fmt.Errorf("config: %s: cluster selector matched no clusters", path)
	}
	elements := make([]map[string]any, 0, len(selected))
	for _, c := range selected {
		elements = append(elements, clusterElement(c))
	}
	return expandFromElements(set, path, elements)
}

// clusterElement flattens a registry entry into template placeholders:
// name, kubeconfig, context, cluster.name, cluster.kubeconfig, cluster.context,
// and cluster.labels.<key> for each label.
func clusterElement(c Cluster) map[string]any {
	el := map[string]any{
		"name":               c.Name,
		"kubeconfig":         c.Kubeconfig,
		"context":            c.Context,
		"cluster.name":       c.Name,
		"cluster.kubeconfig": c.Kubeconfig,
		"cluster.context":    c.Context,
	}
	for k, v := range c.Labels {
		el["cluster.labels."+k] = v
	}
	return el
}

func expandFromElements(set rolloutSetDoc, path string, elements []map[string]any) ([]NamedConfig, error) {
	if len(elements) == 0 {
		return nil, fmt.Errorf("config: %s: generator produced no elements", path)
	}
	out := make([]NamedConfig, 0, len(elements))
	seenRef := make(map[string]string, len(elements))
	for i, el := range elements {
		name := stringify(el["name"])
		if name == "" {
			return nil, fmt.Errorf("config: %s: list element %d is missing name", path, i)
		}
		cfg, err := expandOne(set, el, name)
		if err != nil {
			return nil, fmt.Errorf("config: %s#%s: %w", path, name, err)
		}
		ref := cfg.Spec.Target.Ref
		if prev, dup := seenRef[ref]; dup {
			return nil, fmt.Errorf("config: %s: duplicate target.ref %q from elements %s and %s", path, ref, prev, name)
		}
		seenRef[ref] = name
		out = append(out, NamedConfig{Path: path, Config: cfg})
	}
	return out, nil
}

func expandOne(set rolloutSetDoc, el map[string]any, name string) (*Config, error) {
	meta := map[string]any{
		"name": set.Metadata.Name + "-" + name,
	}
	if len(set.Metadata.Labels) > 0 {
		labels := make(map[string]any, len(set.Metadata.Labels))
		for k, v := range set.Metadata.Labels {
			labels[k] = v
		}
		meta["labels"] = labels
	}
	if tm, ok := set.Template["metadata"].(map[string]any); ok {
		for k, v := range tm {
			meta[k] = v
		}
	}
	spec, ok := set.Template["spec"]
	if !ok {
		return nil, fmt.Errorf("template.spec is required")
	}
	doc := map[string]any{
		"apiVersion": SchemaVersion,
		"kind":       Kind,
		"metadata":   meta,
		"spec":       spec,
	}
	raw, err := yaml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("marshal template: %w", err)
	}
	rendered := substitute(string(raw), el)
	if i := strings.Index(rendered, "{{"); i >= 0 {
		end := strings.Index(rendered[i:], "}}")
		ph := rendered[i:]
		if end >= 0 {
			ph = rendered[i : i+end+2]
		}
		return nil, fmt.Errorf("unknown placeholder %s", ph)
	}
	return Load([]byte(rendered))
}

func substitute(s string, el map[string]any) string {
	// Longer keys first so {{cluster.labels.env}} wins over a shorter prefix.
	keys := make([]string, 0, len(el))
	for k := range el {
		keys = append(keys, k)
	}
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if len(keys[j]) > len(keys[i]) {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	for _, k := range keys {
		s = strings.ReplaceAll(s, "{{"+k+"}}", stringify(el[k]))
	}
	return s
}

func stringify(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	default:
		return fmt.Sprint(t)
	}
}
