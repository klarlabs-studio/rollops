package config

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Default config location within a watched repo.
const (
	DefaultBranch = "main"
	DefaultPath   = "rollops.yaml"
)

// maxConfigBytes caps how much of a config file we read. A watched repo is
// attacker-influenced (anyone who can commit to it), so a config committed as a
// symlink to /dev/zero or a multi-GB blob would OOM-crash the daemon on a plain
// os.ReadFile — bypassing per-repo error handling because it's a memory
// exhaustion, not an error return. 1 MiB is far larger than any real rollout
// config yet small enough to never threaten the process.
const maxConfigBytes = 1 << 20 // 1 MiB

// readConfigFile reads a config file with two supply-chain safeguards: it
// rejects symlinks (a repo must not redirect a read to an arbitrary host path or
// device) and caps the read at maxConfigBytes (so an oversized blob cannot
// exhaust memory). It returns an error rather than reading unboundedly.
func readConfigFile(path string) ([]byte, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("config: %s is a symlink; refusing to follow", path)
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("config: %s is not a regular file", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	// Read one byte past the cap so an exactly-cap-plus-one file is still detected
	// as too large rather than silently truncated.
	data, err := io.ReadAll(io.LimitReader(f, maxConfigBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxConfigBytes {
		return nil, fmt.Errorf("config: %s exceeds %d bytes", path, maxConfigBytes)
	}
	return data, nil
}

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
	data, err := readConfigFile(full)
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
// many apps — the keel-style "manage everything" layout. A kind: RolloutSet file
// expands in memory into N ordinary RolloutConfigs (list or cluster generator).
// Each file is validated; one bad file fails the whole load so a broken config
// never silently drops an app from reconciliation.
func LoadAllFromDir(dir string, r RepoRef) ([]NamedConfig, error) {
	r = r.WithDefaults()
	full := filepath.Join(dir, filepath.Clean("/" + r.Path)[1:]) // prevent path escape
	info, err := os.Stat(full)
	if err != nil {
		return nil, fmt.Errorf("config: stat %s: %w", r.Path, err)
	}
	if !info.IsDir() {
		data, err := readConfigFile(full)
		if err != nil {
			return nil, fmt.Errorf("config: read %s: %w", r.Path, err)
		}
		return loadDocuments(data, r.Path)
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
		data, err := readConfigFile(filepath.Join(full, name))
		if err != nil {
			return nil, fmt.Errorf("config: read %s/%s: %w", r.Path, name, err)
		}
		docs, err := loadDocuments(data, filepath.Join(r.Path, name))
		if err != nil {
			return nil, err
		}
		out = append(out, docs...)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("config: no .yaml configs in %s", r.Path)
	}
	return out, nil
}
