package kubernetes

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

// TraefikRouterMiddlewaresAnnotation is the Ingress annotation Traefik reads for
// the middleware chain. A value that names a Middleware which is neither in the
// same apply batch nor already live in the cluster leaves Traefik unable to
// build the router — the #182 apex-domain 404.
const TraefikRouterMiddlewaresAnnotation = "traefik.ingress.kubernetes.io/router.middlewares"

// ExistsFunc reports whether a Middleware is already live in the cluster.
// A nil ExistsFunc is treated as "not found" for every candidate.
type ExistsFunc func(namespace, name string) (bool, error)

type mwNeed struct {
	ingressNS, ingressName, token string
}

// DanglingMiddlewareWarnings scans rendered Kubernetes manifests for Ingress
// objects whose router.middlewares annotation names a Middleware that is
// neither declared in the same batch nor present in the cluster.
//
// Warnings are strings suitable for an operator log. This never returns an
// error meant to block apply — a dangling reference is almost always a
// mistake, but refusing the batch here would reinvent Preflight.
func DanglingMiddlewareWarnings(manifests [][]byte, exists ExistsFunc) []string {
	inBatch := map[string]bool{} // "ns/name"
	var needs []mwNeed
	for _, raw := range manifests {
		scanManifestDocs(raw, inBatch, &needs)
	}
	var out []string
	seen := map[string]bool{}
	for _, n := range needs {
		if middlewareResolved(n.token, n.ingressNS, inBatch, exists) {
			continue
		}
		warn := fmt.Sprintf(
			"Ingress %s/%s names Middleware %q via %s, but it is neither in this batch nor live in the cluster",
			n.ingressNS, n.ingressName, n.token+"@kubernetescrd", TraefikRouterMiddlewaresAnnotation,
		)
		if seen[warn] {
			continue
		}
		seen[warn] = true
		out = append(out, warn)
	}
	return out
}

func scanManifestDocs(raw []byte, inBatch map[string]bool, needs *[]mwNeed) {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	for {
		var doc map[string]any
		err := dec.Decode(&doc)
		if err == io.EOF {
			return
		}
		if err != nil || doc == nil {
			continue
		}
		kind, _ := doc["kind"].(string)
		meta, _ := doc["metadata"].(map[string]any)
		if meta == nil {
			continue
		}
		name, _ := meta["name"].(string)
		ns, _ := meta["namespace"].(string)
		switch kind {
		case "Middleware":
			if name == "" {
				continue
			}
			if ns == "" {
				ns = "default"
			}
			inBatch[ns+"/"+name] = true
		case "Ingress":
			if name == "" {
				continue
			}
			if ns == "" {
				ns = "default"
			}
			ann, _ := meta["annotations"].(map[string]any)
			if ann == nil {
				continue
			}
			rawAnn, _ := ann[TraefikRouterMiddlewaresAnnotation].(string)
			for _, token := range splitMiddlewareAnnotation(rawAnn) {
				*needs = append(*needs, mwNeed{ingressNS: ns, ingressName: name, token: token})
			}
		}
	}
}

// splitMiddlewareAnnotation returns each comma-separated Traefik middleware
// token that uses the kubernetes CRD provider (`…@kubernetescrd`), without the
// provider suffix. Other providers are ignored — Rollops cannot see them.
func splitMiddlewareAnnotation(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		namePart, provider, ok := strings.Cut(part, "@")
		if !ok || provider != "kubernetescrd" {
			continue
		}
		namePart = strings.TrimSpace(namePart)
		if namePart == "" {
			continue
		}
		out = append(out, namePart)
	}
	return out
}

// middlewareResolved reports whether a Traefik CRD middleware token
// (`<name>` or `<namespace>-<name>`) matches a Middleware in the batch or
// the live cluster.
//
// Traefik encodes cross-namespace refs as `<namespace>-<middleware>@kubernetescrd`.
// Both sides may contain hyphens, so we do not pick a single split. Instead we
// try the same-namespace short form and every hyphen split against the batch
// and, when provided, the cluster.
func middlewareResolved(token, ingressNS string, inBatch map[string]bool, exists ExistsFunc) bool {
	if liveOrBatch(ingressNS, token, inBatch, exists) {
		return true
	}
	for _, i := range hyphenIndexes(token) {
		ns, name := token[:i], token[i+1:]
		if ns == "" || name == "" {
			continue
		}
		if liveOrBatch(ns, name, inBatch, exists) {
			return true
		}
	}
	return false
}

func liveOrBatch(ns, name string, inBatch map[string]bool, exists ExistsFunc) bool {
	if inBatch[ns+"/"+name] {
		return true
	}
	if exists == nil {
		return false
	}
	ok, err := exists(ns, name)
	return err == nil && ok
}

func hyphenIndexes(s string) []int {
	var out []int
	for i := 0; i < len(s); i++ {
		if s[i] == '-' {
			out = append(out, i)
		}
	}
	return out
}
