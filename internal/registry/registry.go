// Package registry is the Rollops plugin marketplace client: it fetches a
// curated plugin index, lets the CLI search it, and resolves a plugin name +
// version to a platform-specific, sha256-pinned (optionally cosign-signed)
// release artifact for `rollops plugin install <name>`.
package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
)

// DefaultURL is the curated index published in the rollops repo. Override with
// ROLLOPS_PLUGIN_REGISTRY or the --registry flag (e.g. a private index).
const DefaultURL = "https://raw.githubusercontent.com/klarlabs-studio/rollops/main/registry/plugins.json"

// URL returns the effective registry URL (env override, else default).
func URL() string {
	if v := os.Getenv("ROLLOPS_PLUGIN_REGISTRY"); v != "" {
		return v
	}
	return DefaultURL
}

// Index is the registry document.
type Index struct {
	Plugins []Plugin `json:"plugins"`
}

// Plugin is one published plugin and its released versions.
type Plugin struct {
	Name         string             `json:"name"`
	Description  string             `json:"description"`
	Homepage     string             `json:"homepage"`
	Capabilities []string           `json:"capabilities"`
	Latest       string             `json:"latest"`
	Versions     map[string]Version `json:"versions"`
}

// Version is one release: optional cosign identity plus per-platform artifacts.
type Version struct {
	Cosign    *Cosign    `json:"cosign,omitempty"`
	Artifacts []Artifact `json:"artifacts"`
}

// Cosign is the keyless verification material for a signed release.
type Cosign struct {
	Identity string `json:"identity"`
	Issuer   string `json:"issuer"`
}

// Artifact is a downloadable binary for one OS/arch with its sha256 pin.
type Artifact struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

// Fetch loads and parses the registry index from url.
func Fetch(ctx context.Context, hc *http.Client, url string) (Index, error) {
	if hc == nil {
		hc = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Index{}, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return Index{}, fmt.Errorf("registry: fetch %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Index{}, fmt.Errorf("registry: fetch %s: status %d", url, resp.StatusCode)
	}
	var idx Index
	if err := json.NewDecoder(resp.Body).Decode(&idx); err != nil {
		return Index{}, fmt.Errorf("registry: parse: %w", err)
	}
	return idx, nil
}

// Find returns the named plugin, or false.
func (i Index) Find(name string) (Plugin, bool) {
	for _, p := range i.Plugins {
		if p.Name == name {
			return p, true
		}
	}
	return Plugin{}, false
}

// Search returns plugins whose name, description, or capabilities match q
// (case-insensitive substring); an empty q returns all. Sorted by name.
func (i Index) Search(q string) []Plugin {
	q = strings.ToLower(strings.TrimSpace(q))
	var out []Plugin
	for _, p := range i.Plugins {
		if q == "" || strings.Contains(strings.ToLower(p.Name), q) ||
			strings.Contains(strings.ToLower(p.Description), q) ||
			strings.Contains(strings.ToLower(strings.Join(p.Capabilities, " ")), q) {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out
}

// Resolve picks the artifact for (name, version, os, arch). An empty version
// uses the plugin's `latest`. Returns the artifact and the version's cosign
// material (nil if the release is unsigned).
func (i Index) Resolve(name, version, goos, goarch string) (Artifact, *Cosign, error) {
	p, ok := i.Find(name)
	if !ok {
		return Artifact{}, nil, fmt.Errorf("registry: plugin %q not found", name)
	}
	if version == "" {
		version = p.Latest
	}
	v, ok := p.Versions[version]
	if !ok {
		return Artifact{}, nil, fmt.Errorf("registry: plugin %q has no version %q", name, version)
	}
	for _, a := range v.Artifacts {
		if a.OS == goos && a.Arch == goarch {
			return a, v.Cosign, nil
		}
	}
	return Artifact{}, nil, fmt.Errorf("registry: plugin %q %s: no artifact for %s/%s", name, version, goos, goarch)
}
