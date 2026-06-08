package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// Default config location within a watched repo.
const (
	DefaultBranch = "main"
	DefaultPath   = "rolloffs.yaml"
)

// RepoRef addresses one watched repo's config: the repo, the branch to track,
// and the config path within it. Multi-tenancy is a property of this Git
// structure — one repo per customer/service, each isolated.
type RepoRef struct {
	URL    string // git remote URL (or local path)
	Branch string // branch to track; defaults to main
	Path   string // config path within the repo; defaults to rolloffs.yaml
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
