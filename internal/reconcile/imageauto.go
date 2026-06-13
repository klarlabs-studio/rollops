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

// TagLister lists a repository's published tags and resolves a tag's manifest
// digest. imageupdate.Scanner implements it; tests inject a fake.
type TagLister interface {
	Tags(ctx context.Context, image string) ([]string, error)
	Digest(ctx context.Context, image string) (string, error)
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

	// Resolve the new image reference per mode: digest mode pins a mutable tag
	// (latest) to its current manifest digest and redeploys when it changes (keel
	// "force"); the semver modes pick the highest qualifying tag.
	newRef, from, to, err := ia.resolve(ctx, image, pol)
	if err != nil || newRef == "" {
		return nc.Config, "", err
	}

	path := filepath.Join(src.Dir(), nc.Path)
	data, err := os.ReadFile(path)
	if err != nil {
		return nc.Config, "", fmt.Errorf("imageauto: read %s: %w", nc.Path, err)
	}
	patched, changed, err := imageupdate.PatchRolloutImageRef(data, newRef)
	if err != nil {
		return nc.Config, "", err
	}
	if !changed {
		return nc.Config, "", nil
	}
	msg := fmt.Sprintf("chore(image): %s %s -> %s (rollops)", nc.Config.Metadata.Name, from, to)
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
	return bumped, newRef, nil
}

// resolve computes the new image reference for the current image and policy, or
// returns newRef="" when nothing changed. from/to are the human-readable old/new
// versions for the commit message.
func (ia ImageAuto) resolve(ctx context.Context, image string, pol *config.ImagePolicy) (newRef, from, to string, err error) {
	if pol.Mode == "digest" {
		repo, tag, oldDigest := splitDigestRef(image)
		mutable := repo + ":" + tag
		newDigest, derr := ia.Scanner.Digest(ctx, mutable)
		if derr != nil {
			return "", "", "", fmt.Errorf("imageauto: digest %s: %w", mutable, derr)
		}
		if newDigest == oldDigest {
			return "", "", "", nil
		}
		return mutable + "@" + newDigest, shortDigest(oldDigest), shortDigest(newDigest), nil
	}
	// semver modes
	repo, curTag := splitImage(image)
	tags, terr := ia.Scanner.Tags(ctx, repo)
	if terr != nil {
		return "", "", "", fmt.Errorf("imageauto: scan %s: %w", repo, terr)
	}
	mode := pol.Mode
	if mode == "any" {
		mode = "major"
	}
	newTag, ok := imageupdate.SelectTag(curTag, tags, mode, pol.Pattern)
	if !ok {
		return "", "", "", nil
	}
	check := imageupdate.Policy{TagPattern: pol.Pattern, AllowMutableTags: pol.AllowMutableTags}
	if verr := check.Validate(imageupdate.Update{Image: repo, Tag: newTag}); verr != nil {
		return "", "", "", verr
	}
	return repo + ":" + newTag, curTag, newTag, nil
}

// splitImage splits image:tag, defaulting the tag to "latest".
func splitImage(image string) (repo, tag string) {
	if i := strings.LastIndexByte(image, ':'); i > strings.LastIndexByte(image, '/') {
		return image[:i], image[i+1:]
	}
	return image, "latest"
}

// splitDigestRef parses repo:tag@sha256:digest (tag and digest optional),
// returning repo, mutable tag (default latest), and the pinned digest if any.
func splitDigestRef(ref string) (repo, tag, digest string) {
	if at := strings.IndexByte(ref, '@'); at >= 0 {
		digest = ref[at+1:]
		ref = ref[:at]
	}
	repo, tag = splitImage(ref)
	return repo, tag, digest
}

func shortDigest(d string) string {
	if d == "" {
		return "none"
	}
	if i := strings.IndexByte(d, ':'); i >= 0 && len(d) > i+13 {
		return d[:i+13]
	}
	return d
}
