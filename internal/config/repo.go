package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// Default config location within a watched repo.
const (
	DefaultBranch = "main"
	DefaultPath   = "rollops.yaml"
)

// RepoRef addresses one watched repo's config: the repo, the branch to track,
// and the config path within it. Multi-tenancy is a property of this Git
// structure — one repo per customer/service, each isolated.
type RepoRef struct {
	URL    string // git remote URL (or local path)
	Branch string // branch to track; defaults to main
	Path   string // config path within the repo; defaults to rollops.yaml
}

// WithDefaults fills branch and path when unset.
func (r RepoRef) WithDefaults() RepoRef {
	if r.Branch == "" {
		r.Branch = DefaultBranch
	}
	if r.Path == "" {
		r.Path = DefaultPath
	}
	return r
}

// LoadFromDir loads and validates the config at r.Path inside an already
// checked-out working tree rooted at dir. Fetching/checkout is the Git layer's
// job; this resolves the addressed file and runs full validation.
func LoadFromDir(dir string, r RepoRef) (*Config, error) {
	r = r.WithDefaults()
	full := filepath.Join(dir, filepath.Clean("/" + r.Path)[1:]) // prevent path escape
	data, err := os.ReadFile(full)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", r.Path, err)
	}
	c, err := Load(data)
	if err != nil {
		return nil, fmt.Errorf("config: %s: %w", r.Path, err)
	}
	return c, nil
}

// NamedConfig pairs a loaded config with the repo-relative path it came from.
type NamedConfig struct {
	Path   string
	Config *Config
}

// LoadAllFromDir loads every rollout config addressed by r.Path. When r.Path is a
// single file it returns that one config; when it is a directory it loads all
// *.yaml / *.yml files in it (sorted, non-recursive), so one repo path can hold
// many apps — the keel-style "manage everything" layout. Each file is validated;
// one bad file fails the whole load so a broken config never silently drops an
// app from reconciliation.
func LoadAllFromDir(dir string, r RepoRef) ([]NamedConfig, error) {
	r = r.WithDefaults()
	full := filepath.Join(dir, filepath.Clean("/" + r.Path)[1:]) // prevent path escape
	info, err := os.Stat(full)
	if err != nil {
		return nil, fmt.Errorf("config: stat %s: %w", r.Path, err)
	}
	if !info.IsDir() {
		data, err := os.ReadFile(full)
		if err != nil {
			return nil, fmt.Errorf("config: read %s: %w", r.Path, err)
		}
		c, err := Load(data)
		if err != nil {
			return nil, fmt.Errorf("config: %s: %w", r.Path, err)
		}
		return []NamedConfig{{Path: r.Path, Config: c}}, nil
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		return nil, fmt.Errorf("config: read dir %s: %w", r.Path, err)
	}
	var out []NamedConfig
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if ext := filepath.Ext(name); ext != ".yaml" && ext != ".yml" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(full, name))
		if err != nil {
			return nil, fmt.Errorf("config: read %s/%s: %w", r.Path, name, err)
		}
		c, err := Load(data)
		if err != nil {
			return nil, fmt.Errorf("config: %s/%s: %w", r.Path, name, err)
		}
		out = append(out, NamedConfig{Path: filepath.Join(r.Path, name), Config: c})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("config: no .yaml configs in %s", r.Path)
	}
	return out, nil
}
