package reconcile

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.klarlabs.de/rollops/internal/config"
	"go.klarlabs.de/rollops/internal/git"
	"go.klarlabs.de/rollops/internal/imageupdate"
)

// TagLister lists a repository's published tags. imageupdate.Scanner implements
// it; tests inject a fake.
type TagLister interface {
	Tags(ctx context.Context, image string) ([]string, error)
}

// ImageAuto performs registry-poll image automation for a config that carries an
// imagePolicy: it scans the registry for newer tags of the tracked image and,
// per policy, writes a bumped image back to Git. Git stays the source of truth —
// the bump is a commit, which the reconcile loop then deploys. This is the
// GitOps replacement for keel's registry watching.
type ImageAuto struct {
	Scanner TagLister
}

// Process scans nc's tracked image and, when a newer tag qualifies, patches the
// config file in src, commits, and pushes. It returns the config to reconcile
// (the bumped one when it changed, else the original) and the new image ref.
func (ia ImageAuto) Process(ctx context.Context, src *git.Source, nc config.NamedConfig) (*config.Config, string, error) {
	pol := nc.Config.Spec.ImagePolicy
	if pol == nil {
		return nc.Config, "", nil
	}
	image, _ := nc.Config.Spec.Target.Spec["image"].(string)
	if image == "" {
		return nc.Config, "", fmt.Errorf("imageauto: imagePolicy set but spec.target.spec.image is empty")
	}
	repo, curTag := splitImage(image)
	tags, err := ia.Scanner.Tags(ctx, repo)
	if err != nil {
		return nc.Config, "", fmt.Errorf("imageauto: scan %s: %w", repo, err)
	}
	mode := pol.Mode
	if mode == "any" {
		mode = "major"
	}
	newTag, ok := imageupdate.SelectTag(curTag, tags, mode, pol.Pattern)
	if !ok {
		return nc.Config, "", nil // already current
	}
	// Re-check against the safety policy before writing anything to Git.
	check := imageupdate.Policy{TagPattern: pol.Pattern, AllowMutableTags: pol.AllowMutableTags}
	if err := check.Validate(imageupdate.Update{Image: repo, Tag: newTag}); err != nil {
		return nc.Config, "", err
	}

	path := filepath.Join(src.Dir(), nc.Path)
	data, err := os.ReadFile(path)
	if err != nil {
		return nc.Config, "", fmt.Errorf("imageauto: read %s: %w", nc.Path, err)
	}
	patched, changed, err := imageupdate.PatchRolloutImage(data, repo, newTag)
	if err != nil {
		return nc.Config, "", err
	}
	if !changed {
		return nc.Config, "", nil
	}
	msg := fmt.Sprintf("chore(image): %s %s -> %s (rollops)", nc.Config.Metadata.Name, curTag, newTag)
	if _, _, err := src.CommitFile(ctx, nc.Path, patched, msg); err != nil {
		return nc.Config, "", fmt.Errorf("imageauto: commit: %w", err)
	}
	if err := src.Push(ctx); err != nil {
		return nc.Config, "", fmt.Errorf("imageauto: push: %w", err)
	}
	bumped, err := config.Load(patched)
	if err != nil {
		return nc.Config, "", fmt.Errorf("imageauto: reload bumped config: %w", err)
	}
	return bumped, repo + ":" + newTag, nil
}

// splitImage splits image:tag, defaulting the tag to "latest".
func splitImage(image string) (repo, tag string) {
	if i := strings.LastIndexByte(image, ':'); i > strings.LastIndexByte(image, '/') {
		return image[:i], image[i+1:]
	}
	return image, "latest"
}
