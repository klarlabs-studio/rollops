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
	// digest→semver migration: a digest-pinned ref under a semver policy can't be
	// compared as a version (splitImage would choke on the @sha256: suffix). Once,
	// convert it to the semver tag the pinned digest points at, so ordinary semver
	// automation takes over from the next tick. Git stays the source of truth.
	if strings.ContainsRune(image, '@') {
		return ia.migrate(ctx, image, pol)
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

// migrate converts a digest-pinned image ref to a semver-tracked tag. If the ref
// already carries a semver tag (repo:v1.2.3@sha256:…) it trusts that tag and just
// strips the digest. Otherwise (repo@sha256:… or repo:latest@sha256:…) it
// reverse-looks-up the registry for the highest semver tag whose manifest digest
// equals the pinned one. A faithful conversion: the same image, now expressed as
// a version. Returns an error when no semver tag points at the pinned digest —
// best-effort, so the reconcile loop logs it and never blocks.
func (ia ImageAuto) migrate(ctx context.Context, image string, pol *config.ImagePolicy) (newRef, from, to string, err error) {
	repo, tag, pinned := splitDigestRef(image)
	if imageupdate.IsSemver(tag) {
		return repo + ":" + tag, shortDigest(pinned), tag, nil
	}
	if pinned == "" {
		return "", "", "", nil // nothing to migrate from
	}
	tags, terr := ia.Scanner.Tags(ctx, repo)
	if terr != nil {
		return "", "", "", fmt.Errorf("imageauto: scan %s: %w", repo, terr)
	}
	match, ok := ia.semverForDigest(ctx, repo, pinned, tags, pol.Pattern)
	if !ok {
		return "", "", "", fmt.Errorf("imageauto: no semver tag for digest %s of %s", shortDigest(pinned), repo)
	}
	return repo + ":" + match, shortDigest(pinned), match, nil
}

// semverForDigest finds the highest semver tag in tags whose manifest digest
// equals pinned, checking newest first so the first match is the highest version.
func (ia ImageAuto) semverForDigest(ctx context.Context, repo, pinned string, tags []string, pattern string) (string, bool) {
	for _, t := range imageupdate.SemverTagsDesc(tags, pattern) {
		d, err := ia.Scanner.Digest(ctx, repo+":"+t)
		if err != nil {
			continue // can't resolve this tag's digest; try the next
		}
		if d == pinned {
			return t, true
		}
	}
	return "", false
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
