package kubernetes

import (
	"bytes"
	"io"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// PruneLabel marks every resource Rollops manages for a target, so a pruning
// apply (kubectl apply --prune -l) can garbage-collect resources removed from
// desired — the GitOps "delete what's no longer declared" behavior (Flux-style).
const PruneLabel = "rollops.klarlabs.de/target"

var nonLabel = regexp.MustCompile(`[^a-z0-9._-]+`)

// labelValue derives a DNS-1123-safe label value (<=63 chars) from a target ref.
func labelValue(ref string) string {
	v := nonLabel.ReplaceAllString(strings.ToLower(ref), "-")
	v = strings.Trim(v, "-._")
	if len(v) > 63 {
		v = v[:63]
	}
	if v == "" {
		v = "rollops"
	}
	return v
}

// labelManifest injects key=val into every document's metadata.labels so the
// whole set is selectable for pruning. Multi-doc YAML is preserved doc-by-doc.
func labelManifest(manifest []byte, key, val string) ([]byte, error) {
	dec := yaml.NewDecoder(bytes.NewReader(manifest))
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	for {
		var doc map[string]any
		err := dec.Decode(&doc)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if doc == nil {
			continue
		}
		meta, _ := doc["metadata"].(map[string]any)
		if meta == nil {
			meta = map[string]any{}
			doc["metadata"] = meta
		}
		labels, _ := meta["labels"].(map[string]any)
		if labels == nil {
			labels = map[string]any{}
			meta["labels"] = labels
		}
		labels[key] = val
		if err := enc.Encode(doc); err != nil {
			return nil, err
		}
	}
	_ = enc.Close()
	return out.Bytes(), nil
}
