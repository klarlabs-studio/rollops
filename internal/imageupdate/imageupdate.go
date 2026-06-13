package imageupdate

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Policy constrains automated image updates before they are written to Git.
type Policy struct {
	AllowedRegistries []string
	TagPattern        string
	AllowMutableTags  bool
}

// Update is one proposed image replacement.
type Update struct {
	Image string
	Tag   string
}

func (p Policy) Validate(u Update) error {
	if u.Image == "" || u.Tag == "" {
		return fmt.Errorf("imageupdate: image and tag are required")
	}
	if !p.AllowMutableTags && (u.Tag == "latest" || u.Tag == "main" || u.Tag == "master") {
		return fmt.Errorf("imageupdate: mutable tag %q is not allowed", u.Tag)
	}
	if len(p.AllowedRegistries) > 0 {
		reg := registryOf(u.Image)
		allowed := false
		for _, a := range p.AllowedRegistries {
			if reg == a {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("imageupdate: registry %q is not allowed", reg)
		}
	}
	if p.TagPattern != "" {
		ok, err := regexp.MatchString(p.TagPattern, u.Tag)
		if err != nil {
			return fmt.Errorf("imageupdate: tagPattern: %w", err)
		}
		if !ok {
			return fmt.Errorf("imageupdate: tag %q does not match policy", u.Tag)
		}
	}
	return nil
}

func registryOf(image string) string {
	first, _, _ := strings.Cut(image, "/")
	if strings.Contains(first, ".") || strings.Contains(first, ":") || first == "localhost" {
		return first
	}
	return "docker.io"
}

// PatchRolloutImage updates spec.target.spec.image in a Rollops YAML document.
func PatchRolloutImage(data []byte, image, tag string) ([]byte, bool, error) {
	if image == "" || tag == "" {
		return nil, false, fmt.Errorf("imageupdate: image and tag are required")
	}
	return PatchRolloutImageRef(data, image+":"+tag)
}

// PatchRolloutImageRef sets spec.target.spec.image to an arbitrary reference
// (e.g. a digest-pinned `repo:tag@sha256:…`), returning whether it changed.
func PatchRolloutImageRef(data []byte, ref string) ([]byte, bool, error) {
	if ref == "" {
		return nil, false, fmt.Errorf("imageupdate: ref is required")
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, false, fmt.Errorf("imageupdate: parse yaml: %w", err)
	}
	node := mappingPath(&doc, "spec", "target", "spec", "image")
	if node == nil {
		return nil, false, fmt.Errorf("imageupdate: spec.target.spec.image not found")
	}
	if node.Value == ref {
		return data, false, nil
	}
	node.Value = ref
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, false, fmt.Errorf("imageupdate: encode yaml: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, false, err
	}
	return out.Bytes(), true, nil
}

func mappingPath(n *yaml.Node, keys ...string) *yaml.Node {
	if n.Kind == yaml.DocumentNode && len(n.Content) == 1 {
		n = n.Content[0]
	}
	for _, key := range keys {
		if n.Kind != yaml.MappingNode {
			return nil
		}
		var next *yaml.Node
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == key {
				next = n.Content[i+1]
				break
			}
		}
		if next == nil {
			return nil
		}
		n = next
	}
	return n
}
