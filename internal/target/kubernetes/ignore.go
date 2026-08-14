package kubernetes

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// kubeNoise is always stripped when evaluating ignoreDifferences so live
// status and server-assigned metadata never count as drift.
var kubeNoise = []string{
	"/status",
	"/metadata/resourceVersion",
	"/metadata/uid",
	"/metadata/generation",
	"/metadata/creationTimestamp",
	"/metadata/managedFields",
	"/metadata/annotations/kubectl.kubernetes.io~1last-applied-configuration",
}

func parseIgnore(s spec) []string {
	raw, ok := s["ignoreDifferences"]
	if !ok || raw == nil {
		return nil
	}
	var out []string
	switch v := raw.(type) {
	case []string:
		out = v
	case []any:
		for _, x := range v {
			if str, ok := x.(string); ok && str != "" {
				out = append(out, str)
			}
		}
	}
	return out
}

// equivalentIgnoring reports whether live and desired are the same after
// stripping Kubernetes noise and the operator's ignoreDifferences pointers.
func equivalentIgnoring(live, desired []byte, ignore []string) (bool, error) {
	var a, b any
	if err := yaml.Unmarshal(live, &a); err != nil {
		return false, fmt.Errorf("kubernetes: parse live yaml: %w", err)
	}
	if err := yaml.Unmarshal(desired, &b); err != nil {
		return false, fmt.Errorf("kubernetes: parse desired yaml: %w", err)
	}
	ptrs := append(append([]string{}, kubeNoise...), ignore...)
	for _, p := range ptrs {
		deleteAt(a, pointerParts(p))
		deleteAt(b, pointerParts(p))
	}
	return reflect.DeepEqual(a, b), nil
}

func pointerParts(p string) []string {
	p = strings.TrimSpace(p)
	if p == "" {
		return nil
	}
	if strings.HasPrefix(p, "/") {
		var parts []string
		for _, s := range strings.Split(p, "/")[1:] {
			s = strings.ReplaceAll(s, "~1", "/")
			s = strings.ReplaceAll(s, "~0", "~")
			if s != "" {
				parts = append(parts, s)
			}
		}
		return parts
	}
	return strings.Split(p, ".")
}

func deleteAt(doc any, parts []string) {
	if doc == nil || len(parts) == 0 {
		return
	}
	switch n := doc.(type) {
	case map[string]any:
		if len(parts) == 1 {
			delete(n, parts[0])
			return
		}
		deleteAt(n[parts[0]], parts[1:])
	case []any:
		i, err := strconv.Atoi(parts[0])
		if err != nil || i < 0 || i >= len(n) {
			return
		}
		if len(parts) == 1 {
			return
		}
		deleteAt(n[i], parts[1:])
	}
}
