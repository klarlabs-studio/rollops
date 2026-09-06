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
// key is always PruneLabel — the identity marker that ties a resource to the
// target that manages it — so it is not a parameter.
func labelManifest(manifest []byte, val string) ([]byte, error) {
	const key = PruneLabel
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

// pruneArgs returns the kubectl flags that make an apply garbage-collect
// resources carrying this target's label but no longer in the apply set.
//
// Split out from Apply so the invariant #158 turns on is testable without a
// cluster: the LABEL goes on every apply, the PRUNE flags only when asked for.
// Conflating them is what left `prune: false` resources unidentifiable.
func pruneArgs(prune bool, pruneVal string) []string {
	if !prune {
		return nil
	}
	return []string{"--prune", "--selector", PruneLabel + "=" + pruneVal}
}

// defaultReapTypes is what a reap deletes when the target does not name types.
//
// `all` is kubectl's shortcut, and it is NARROWER than it reads: pods, services,
// deployments, replicasets, statefulsets, daemonsets, jobs, cronjobs,
// replicationcontrollers. It does NOT include ingresses, configmaps, secrets,
// pvcs, serviceaccounts or CRDs.
//
// That is deliberate and is the safe direction to be wrong in. Reaping too
// little leaves resources behind, which is visible — the orphan report (#154)
// names the target and the operator finishes by hand. Reaping too much deletes
// something nobody asked to lose. A target that manages ingresses or config
// should widen this explicitly via `reapTypes`, which is a decision to take with
// the cluster in front of you rather than a default anyone inherits.
var defaultReapTypes = []string{"all"}

// reapArgs builds the kubectl delete that removes resources carrying this
// target's marker.
//
// Split out from the cluster so the scoping is testable without a cluster: this
// is the one command in rollops that destroys state it did not just create, and
// the selector is the only thing standing between "this target's resources" and
// "the namespace".
func reapArgs(types []string, pruneVal string) []string {
	if len(types) == 0 {
		types = defaultReapTypes
	}
	return []string{
		"delete", strings.Join(types, ","),
		"--selector", PruneLabel + "=" + pruneVal,
		// Deleting nothing is the expected outcome of a retried reap, not an
		// error the caller should retry forever.
		"--ignore-not-found",
	}
}
