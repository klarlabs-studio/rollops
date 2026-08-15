package config

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// KindRolloutSet is a load-time generator: a list of elements plus a template
// expands in memory into ordinary RolloutConfigs. Git holds the template;
// generated configs are ephemeral. Cluster/matrix generators are out of scope.
const KindRolloutSet = "RolloutSet"

type rolloutSetDoc struct {
	APIVersion string                `yaml:"apiVersion"`
	Kind       string                `yaml:"kind"`
	Metadata   Metadata              `yaml:"metadata"`
	Generators []rolloutSetGenerator `yaml:"generators"`
	Template   map[string]any        `yaml:"template"`
}

type rolloutSetGenerator struct {
	List    *listGenerator `yaml:"list,omitempty"`
	Cluster map[string]any `yaml:"cluster,omitempty"`
}

type listGenerator struct {
	Elements []map[string]any `yaml:"elements"`
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

// loadDocuments loads one YAML file as either a RolloutConfig or a RolloutSet
// (expanded in memory into N ordinary configs).
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
	for _, g := range set.Generators {
		if g.Cluster != nil {
			return nil, fmt.Errorf("config: %s: cluster generator is not supported (Phase 1 is list only)", path)
		}
	}
	if len(set.Generators) != 1 || set.Generators[0].List == nil {
		return nil, fmt.Errorf("config: %s: RolloutSet requires exactly one list generator", path)
	}
	elements := set.Generators[0].List.Elements
	if len(elements) == 0 {
		return nil, fmt.Errorf("config: %s: list generator has no elements", path)
	}
	if set.Template == nil {
		return nil, fmt.Errorf("config: %s: RolloutSet template is required", path)
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
	for k, v := range el {
		s = strings.ReplaceAll(s, "{{"+k+"}}", stringify(v))
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
