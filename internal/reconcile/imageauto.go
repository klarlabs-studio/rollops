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

// ImageOutcome names what image automation did for one config in a cycle.
//
// It exists because "nothing to do" and "something should have happened and did
// not" used to be the same word. Process returns ref="" for a pull-request
// proposal as well as for an up-to-date target — deliberately, since the deploy
// waits for the merge — so the reconcile summary reported both as `current`. A
// proposal that never merges therefore reported `current` on every cycle while
// the deploy never happened, which is indistinguishable from healthy. The only
// symptom was the running target quietly serving an old image.
type ImageOutcome string

const (
	// ImageOutcomeDisabled: the config carries no imagePolicy, or mode: none. No
	// check was performed — reporting that as `current` claims one was.
	ImageOutcomeDisabled ImageOutcome = "disabled"
	// ImageOutcomeCurrent: the registry offers nothing Git does not already pin.
	// This is the only outcome that means "nothing to do".
	ImageOutcomeCurrent ImageOutcome = "current"
	// ImageOutcomeBumped: the tracked branch now carries the bump; it deploys this
	// cycle.
	ImageOutcomeBumped ImageOutcome = "bumped"
	// ImageOutcomeProposed: a pull request carrying the bump was opened or
	// refreshed. Git has NOT adopted it yet, so the deploy waits on the merge —
	// a target that stays here across cycles is stuck, not healthy.
	ImageOutcomeProposed ImageOutcome = "proposed"
	// ImageOutcomePending: a newer image exists and the proposal branch already
	// carries exactly it, so nothing was pushed. Git has still not adopted it.
	ImageOutcomePending ImageOutcome = "pending"
	// ImageOutcomePinned: the config file already contains the computed ref, so
	// Git was not rewritten. Distinct from current — current means the scanner
	// identity equals the Git pin, not "patch was a no-op".
	ImageOutcomePinned ImageOutcome = "pinned"
	// ImageOutcomeError: the cycle failed; see the accompanying error.
	ImageOutcomeError ImageOutcome = "error"
)

// Deployed reports whether the tracked branch now carries the bump.
func (o ImageOutcome) Deployed() bool { return o == ImageOutcomeBumped }

// AwaitingGit reports whether a newer image exists that Git has not adopted.
// These are the outcomes that look idle but are not: left unattended they mean
// a target never receives an image the registry has been offering.
func (o ImageOutcome) AwaitingGit() bool {
	return o == ImageOutcomeProposed || o == ImageOutcomePending
}

// ImageStatus is what image automation did for one config, and what it saw
// while doing it.
//
// The verdict alone proved undiagnosable in production: a target reported
// `current` for eighteen hours while the registry had moved, and nothing
// recorded what had actually been resolved at the time, so afterwards there was
// no way to tell a correct match from a bad resolution. Carrying the
// observation next to the verdict makes `current` checkable rather than
// something to be trusted.
type ImageStatus struct {
	Outcome ImageOutcome

	// Resolved is the identity the registry currently offers for the tracked
	// reference: the manifest digest in digest mode, the selected tag in the
	// semver modes. Empty when nothing was resolved (disabled, or an error
	// before the registry was reached).
	Resolved string

	// Pinned is what Git pins today, in the same terms as Resolved.
	Pinned string
}

// withOutcome stamps an outcome onto an observation.
func withOutcome(s ImageStatus, o ImageOutcome) ImageStatus { s.Outcome = o; return s }

// Short renders the observation compactly for a reconcile summary, as
// "(resolved==pinned)" when they agree and "(resolved!=pinned)" when they do
// not. Empty when nothing was resolved.
func (s ImageStatus) Short() string {
	if s.Resolved == "" {
		return ""
	}
	if s.Resolved == s.Pinned {
		return "(" + shortDigest(s.Resolved) + ")"
	}
	return "(" + shortDigest(s.Pinned) + "->" + shortDigest(s.Resolved) + ")"
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
func (ia ImageAuto) Process(ctx context.Context, src *git.Source, nc config.NamedConfig) (*config.Config, string, ImageStatus, error) {
	pol := nc.Config.Spec.ImagePolicy
	if pol == nil || pol.Mode == "none" {
		// No automation: the image committed in Git is authoritative. "none" makes
		// this explicit for digest-pinned configs that must not be re-bumped from a
		// mutable tag.
		return nc.Config, "", ImageStatus{Outcome: ImageOutcomeDisabled}, nil
	}
	image, _ := nc.Config.Spec.Target.Spec["image"].(string)
	if image == "" {
		return nc.Config, "", ImageStatus{Outcome: ImageOutcomeError}, fmt.Errorf("imageauto: imagePolicy set but spec.target.spec.image is empty")
	}

	// Resolve the new image reference per mode: digest mode pins a mutable tag
	// (latest) to its current manifest digest and redeploys when it changes (keel
	// "force"); the semver modes pick the highest qualifying tag.
	res, err := ia.resolve(ctx, image, pol)
	seen := ImageStatus{Resolved: res.Resolved, Pinned: res.Pinned}
	if err != nil {
		return nc.Config, "", withOutcome(seen, ImageOutcomeError), err
	}
	if res.Ref == "" {
		if err := requireCurrentIdentities(seen); err != nil {
			return nc.Config, "", withOutcome(seen, ImageOutcomeError), err
		}
		return nc.Config, "", withOutcome(seen, ImageOutcomeCurrent), nil
	}
	newRef, from, to := res.Ref, res.From, res.To

	path := filepath.Join(src.Dir(), nc.Path)
	data, err := os.ReadFile(path)
	if err != nil {
		return nc.Config, "", withOutcome(seen, ImageOutcomeError), fmt.Errorf("imageauto: read %s: %w", nc.Path, err)
	}
	patched, changed, err := imageupdate.PatchRolloutImageRef(data, newRef)
	if err != nil {
		return nc.Config, "", withOutcome(seen, ImageOutcomeError), err
	}
	if !changed {
		// Scanner moved and the file did not: that combination is an error, never
		// current (#98). File-already-pins (identities agree, patch is a no-op)
		// is pinned — a distinct outcome from "registry matches Git".
		if err := requireCurrentIdentities(seen); err != nil {
			return nc.Config, "", withOutcome(seen, ImageOutcomeError), err
		}
		return nc.Config, "", withOutcome(seen, ImageOutcomePinned), nil
	}
	msg := fmt.Sprintf("chore(image): %s %s -> %s (rollops)", nc.Config.Metadata.Name, from, to)

	if pol.WritebackMode() == config.WritebackPullRequest {
		return ia.proposeViaPR(ctx, src, nc, patched, msg, from, to, seen)
	}

	// Push writeback: commit the bump directly on the tracked branch and deploy
	// it this cycle, since Git and the cluster now agree.
	if _, _, err := src.CommitFile(ctx, nc.Path, patched, msg); err != nil {
		return nc.Config, "", withOutcome(seen, ImageOutcomeError), fmt.Errorf("imageauto: commit: %w", err)
	}
	if err := src.Push(ctx); err != nil {
		return nc.Config, "", withOutcome(seen, ImageOutcomeError), fmt.Errorf("imageauto: push: %w", err)
	}
	bumped, err := config.Load(patched)
	if err != nil {
		return nc.Config, "", withOutcome(seen, ImageOutcomeError), fmt.Errorf("imageauto: reload bumped config: %w", err)
	}
	return bumped, newRef, withOutcome(seen, ImageOutcomeBumped), nil
}

// proposeViaPR is the pull-request writeback path: it commits the bump on a
// rollops-owned branch, pushes it, and opens (or refreshes) a PR into the
// tracked branch — never writing that branch directly, so branch protection is
// honoured.
//
// Crucially it returns ref="" (no deploy). The bump lives only on the PR; the
// cluster must not lead Git. The deploy happens later, through the ordinary
// reconcile, once the PR merges and the tracked branch advances to carry it.
// This is what keeps a protected branch and the running target consistent.
func (ia ImageAuto) proposeViaPR(ctx context.Context, src *git.Source, nc config.NamedConfig, patched []byte, msg, from, to string, seen ImageStatus) (*config.Config, string, ImageStatus, error) {
	head := prBranchName(nc.Config.Metadata.Name)

	// Stop before touching Git when the proposal already stands. Committing
	// would produce a new sha for identical content — the branch is rebuilt
	// from the tracked branch each time — and force-pushing that refreshes the
	// proposal once per reconcile. Where CI cancels in-progress runs per ref,
	// that cancels the very checks the pull request is waiting on, so it can
	// never merge and the bump never deploys.
	if src.RemoteFileMatches(ctx, head, nc.Path, patched) {
		return nc.Config, "", withOutcome(seen, ImageOutcomePending), nil
	}

	committed, err := src.CommitFileOnBranch(ctx, head, nc.Path, patched, msg)
	if err != nil {
		return nc.Config, "", withOutcome(seen, ImageOutcomeError), fmt.Errorf("imageauto: pr commit: %w", err)
	}
	if !committed {
		// The proposal branch already carries exactly this bump; the PR (if any)
		// stands. Nothing to push or reopen — but Git has still not adopted the
		// newer image, so this is pending, never current.
		return nc.Config, "", withOutcome(seen, ImageOutcomePending), nil
	}
	if err := src.PushBranch(ctx, head); err != nil {
		return nc.Config, "", withOutcome(seen, ImageOutcomeError), fmt.Errorf("imageauto: pr push: %w", err)
	}
	body := fmt.Sprintf("Automated image bump by rollops.\n\n- config: `%s`\n- %s → %s\n\nMerging this deploys the new image through the normal reconcile. Opened as a PR because the tracked branch does not accept direct pushes.", nc.Path, from, to)
	if _, _, err := src.OpenPullRequest(ctx, head, src.Branch(), msg, body); err != nil {
		return nc.Config, "", withOutcome(seen, ImageOutcomeError), fmt.Errorf("imageauto: open pr: %w", err)
	}
	return nc.Config, "", withOutcome(seen, ImageOutcomeProposed), nil
}

// prBranchName is the deterministic head branch for a config's image proposals.
// Deterministic so re-running updates the same PR rather than spawning a new one
// each poll; sanitised so an arbitrary config name is always a valid git ref.
func prBranchName(configName string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, configName)
	if safe == "" {
		safe = "config"
	}
	return "rollops/image/" + safe
}

// resolution is what resolve observed and decided.
type resolution struct {
	Ref      string // new image reference, "" when nothing changed
	From, To string // human-readable old/new for the commit message
	Resolved string // what the registry currently offers
	Pinned   string // what Git pins today
}

// resolve computes the new image reference for the current image and policy,
// reporting what it observed even when nothing changed — a verdict without its
// observation cannot be checked later.
func (ia ImageAuto) resolve(ctx context.Context, image string, pol *config.ImagePolicy) (resolution, error) {
	// Bind automation to the allowed registries (when configured) before any
	// network call: a compromised or mistyped image ref must never pull
	// automation toward an unexpected registry.
	if aerr := registryAllowed(image, pol.AllowedRegistries); aerr != nil {
		return resolution{}, aerr
	}

	if pol.Mode == "digest" {
		repo, tag, oldDigest := splitDigestRef(image)
		mutable := repo + ":" + tag
		newDigest, derr := ia.Scanner.Digest(ctx, mutable)
		if derr != nil {
			return resolution{Pinned: oldDigest}, fmt.Errorf("imageauto: digest %s: %w", mutable, derr)
		}
		obs := resolution{Resolved: newDigest, Pinned: oldDigest}
		if newDigest == oldDigest {
			return obs, nil
		}
		obs.Ref = mutable + "@" + newDigest
		obs.From, obs.To = shortDigest(oldDigest), shortDigest(newDigest)
		return obs, nil
	}

	// semver modes. A ref may carry a deliberate digest pin
	// (repo:v1.2.3@sha256:… or repo@sha256:…). That pin MUST survive a semver
	// update — silently emitting an unpinned repo:tag would downgrade an
	// immutable ref to a mutable one and re-open tag-mutation attacks. Parse the
	// pin off and remember whether one was present.
	repo, curTag, curDigest := splitDigestRef(image)
	wasPinned := curDigest != ""

	if imageupdate.IsSemver(curTag) {
		// Ordinary semver selection, whether or not the ref was digest-pinned. The
		// selected tag is re-pinned to its current digest (see selectSemver).
		return ia.selectSemver(ctx, repo, curTag, wasPinned, pol)
	}
	// Non-semver current tag (latest / none). When it is digest-pinned, this is
	// the one-time digest→semver migration: adopt the semver tag the pinned
	// digest corresponds to, keeping the pin.
	if wasPinned {
		return ia.migrate(ctx, repo, curDigest, pol)
	}
	// Unpinned, non-semver current (e.g. repo:latest under a semver policy): there
	// is no version to compare. digest mode is the tool for tracking a mutable tag.
	return resolution{Pinned: curTag}, nil
}

// selectSemver picks the highest qualifying semver tag for a semver-tagged
// current ref and returns it digest-pinned, so all semver modes now emit an
// immutable reference. wasPinned records whether the current ref carried a
// digest so the pin can never be silently dropped (see pinnedRef).
func (ia ImageAuto) selectSemver(ctx context.Context, repo, curTag string, wasPinned bool, pol *config.ImagePolicy) (resolution, error) {
	tags, terr := ia.Scanner.Tags(ctx, repo)
	if terr != nil {
		return resolution{Pinned: curTag}, fmt.Errorf("imageauto: scan %s: %w", repo, terr)
	}
	mode := pol.Mode
	if mode == "any" {
		mode = "major"
	}
	newTag, ok := imageupdate.SelectTag(curTag, tags, mode, pol.Pattern)
	if !ok {
		// Already current — a pinned ref stays as-is. The observation still
		// records that the highest qualifying tag is the one Git pins.
		return resolution{Resolved: curTag, Pinned: curTag}, nil
	}
	check := imageupdate.Policy{AllowedRegistries: pol.AllowedRegistries, TagPattern: pol.Pattern, AllowMutableTags: pol.AllowMutableTags}
	if verr := check.Validate(imageupdate.Update{Image: repo, Tag: newTag}); verr != nil {
		return resolution{Resolved: newTag, Pinned: curTag}, verr
	}
	ref, perr := ia.pinnedRef(ctx, repo, newTag, wasPinned)
	if perr != nil {
		return resolution{Resolved: newTag, Pinned: curTag}, perr
	}
	return resolution{Ref: ref, From: curTag, To: newTag, Resolved: newTag, Pinned: curTag}, nil
}

// migrate performs the one-time digest→semver conversion for a ref that is
// digest-pinned but not semver-tagged (repo@sha256:… or repo:latest@sha256:…):
// it reverse-looks-up the highest semver tag whose manifest digest equals the
// pinned one and adopts it, keeping the pin (the pinned digest IS that tag's
// digest, so the result is immutable by construction). Fail-closed: when no
// semver tag matches it returns an error rather than dropping or keeping an
// unresolvable pin — best-effort, so the reconcile loop logs it and never blocks.
func (ia ImageAuto) migrate(ctx context.Context, repo, pinned string, pol *config.ImagePolicy) (resolution, error) {
	tags, terr := ia.Scanner.Tags(ctx, repo)
	if terr != nil {
		return resolution{Pinned: pinned}, fmt.Errorf("imageauto: scan %s: %w", repo, terr)
	}
	match, ok := ia.semverForDigest(ctx, repo, pinned, tags, pol.Pattern)
	if !ok {
		return resolution{Pinned: pinned}, fmt.Errorf("imageauto: no semver tag for digest %s of %s", shortDigest(pinned), repo)
	}
	check := imageupdate.Policy{AllowedRegistries: pol.AllowedRegistries, TagPattern: pol.Pattern, AllowMutableTags: pol.AllowMutableTags}
	if verr := check.Validate(imageupdate.Update{Image: repo, Tag: match}); verr != nil {
		return resolution{Resolved: match, Pinned: pinned}, verr
	}
	// The pinned digest is exactly this tag's digest (we matched on it) → keep it.
	return resolution{Ref: repo + ":" + match + "@" + pinned, From: shortDigest(pinned), To: match, Resolved: match, Pinned: pinned}, nil
}

// pinnedRef resolves tag's current manifest digest and returns
// repo:tag@sha256:digest, so a semver update stays immutable. When the current
// ref was digest-pinned, a failed digest resolution is fatal (fail-closed):
// rollops never downgrades an immutable pin to a mutable tag. When the current
// ref was unpinned, pinning is best-effort — on failure it falls back to the
// plain tag, which is no worse than the prior behaviour and never a downgrade.
func (ia ImageAuto) pinnedRef(ctx context.Context, repo, tag string, wasPinned bool) (string, error) {
	digest, derr := ia.Scanner.Digest(ctx, repo+":"+tag)
	if derr == nil && digest != "" {
		return repo + ":" + tag + "@" + digest, nil
	}
	if wasPinned {
		if derr == nil {
			derr = fmt.Errorf("registry returned no digest")
		}
		return "", fmt.Errorf("imageauto: refusing to unpin %s:%s across a semver update: %w", repo, tag, derr)
	}
	return repo + ":" + tag, nil
}

// registryAllowed enforces the optional per-config registry allowlist. An empty
// list means no restriction. The image ref's registry host (via
// imageupdate.ParseRef) must exactly match one of the allowed hosts.
func registryAllowed(image string, allowed []string) error {
	if len(allowed) == 0 {
		return nil
	}
	host, _ := imageupdate.ParseRef(image)
	for _, a := range allowed {
		if host == a {
			return nil
		}
	}
	return fmt.Errorf("imageauto: image registry %q is not in allowedRegistries %v", host, allowed)
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

// requireCurrentIdentities is the #98 invariant: current (and the patch-no-op
// pinned path) are only legal when the scanner identity equals the Git pin.
// A mismatch with nothing written is an error, never current.
func requireCurrentIdentities(s ImageStatus) error {
	if s.Resolved == "" || s.Pinned == "" || s.Resolved == s.Pinned {
		return nil
	}
	return fmt.Errorf("imageauto: scanner %s does not match Git pin %s", shortDigest(s.Resolved), shortDigest(s.Pinned))
}
