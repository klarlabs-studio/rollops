# Image Automation

Image automation is a Phase 2 helper for agent- or CI-driven updates. Rollops
does not poll registries in core; it validates a proposed image/tag update,
patches the rollout YAML, and writes the desired-state change back to Git.

## Policy

`internal/imageupdate.Policy` checks:

- allowed registries
- mutable tag rejection (`latest`, `main`, `master`) unless explicitly allowed
- optional tag regex

Rejected updates never reach Git.

## YAML Patch

`PatchRolloutImage` updates:

```yaml
spec:
  target:
    spec:
      image: ghcr.io/klarlabs/api:v1.2.3
```

This keeps Git as the only desired-state source. The Store is not touched.

## Git Writeback

`git.Source.CommitFile` writes a changed rollout config and creates a commit
with the supplied message. It does not push by itself; operators decide which
repos/branches allow writeback and when to publish the commit.

The writeback helper rejects path escapes and no-ops cleanly when content is
unchanged.

## Registry-poll automation (keel-style)

Beyond the validate-and-writeback helper above, rollopsd can **poll the registry**
itself and bump images automatically — the keel model, but GitOps (Git stays the
source of truth). Add an `imagePolicy` to a rollout config and set the tracked
image as `spec.target.spec.image`:

```yaml
spec:
  target:
    kind: kubernetes
    spec:
      namespace: prod
      resource: deployment/web
      image: ghcr.io/acme/web:v1.4.0   # tracked image (overrides the manifest's container)
      manifest: |
        ...
  imagePolicy:
    mode: minor            # major | minor | patch | any
    pattern: '^v\d'        # optional tag regexp
    allowMutableTags: false
```

Each reconcile tick, for every config with an `imagePolicy`, rollopsd scans the
registry (Docker Registry v2 + bearer challenge; works for ghcr.io, Docker Hub,
…), selects the newest tag allowed by `mode`, and if it is newer than the tracked
tag, **patches `spec.target.spec.image` and commits + pushes** the change to the
config repo. The next reconcile deploys it. The `image` field overrides the
container image of matching containers in the rendered manifest, so the bump
reaches the workload.

Enable it on the daemon with `ROLLOPS_IMAGE_AUTOMATION=1`. Private registries:
`ROLLOPS_REGISTRY_USER` / `ROLLOPS_REGISTRY_TOKEN`. Writeback needs a token with
push access on the config repo (see `docs/deploy-kubernetes.md`).

## Writeback to a protected branch

By default the bump is **committed and pushed directly** to the tracked branch.
That fails on a **protected branch** — one requiring pull requests or status
checks — because rollopsd's direct push is rejected (`GH006: Changes must be
made through a pull request`). The symptom is quiet: the config shows `=error`
in the reconcile summary and the target simply never updates.

Set `imagePolicy.writeback: pull-request` for those repos:

```yaml
imagePolicy:
  mode: digest
  allowMutableTags: true
  writeback: pull-request   # default: push
```

In this mode rollopsd never writes the tracked branch. It pushes the bump to a
deterministic branch (`rollops/image/<config-name>`) and opens a PR into the
tracked branch, enabling GitHub **auto-merge** so the change lands the moment
its required checks pass. The deploy happens through the ordinary reconcile once
the PR merges and the tracked branch advances — the cluster never leads Git.

The token needs `pull-requests: write` (and `contents: write` for the branch
push). If the repository has auto-merge disabled, the PR is still opened and
waits for a human to merge it — writeback is unblocked either way. GitHub is the
only forge supported for pull-request writeback today.

## Commit-SHA and mutable tags

Not every pipeline publishes semver. Common schemes and the mode to use:

| Image tags published | `imagePolicy.mode` | Tracked `image` |
|----------------------|--------------------|-----------------|
| `:vX.Y.Z` (semver releases) | `minor` / `major` / `patch` | `repo:vX.Y.Z` |
| `:<git-sha>` **plus** `:latest` | `digest` | `repo:latest` |
| `:latest` only | `digest` | `repo:latest` |

`digest` mode resolves the mutable tag's current manifest digest and pins
`repo:tag@sha256:…`, redeploying when the digest moves — so a SHA-per-build
pipeline that also pushes `:latest` auto-updates without any semver. The semver
modes safely **ignore** non-semver tags (commit SHAs, `latest`): they are never
selected and never error, so a mixed tag list is fine.

A pipeline that pushes **only** immutable SHA tags (no `:latest`, no semver) has
no moving reference to track — pin the deployed SHA explicitly, or have CI also
push a mutable tag (`:latest`) or a semver tag to enable automation.

## Digest → semver migration

A digest-pinned `image` under a **semver** policy (`major` / `minor` / `patch` /
`any`) is migrated to a semver-tracked tag automatically — once — so ordinary
semver bumps take over. This lets you adopt semver automation on a workload that
was previously pinned to a digest, without hand-editing the ref:

| Tracked `image` | Migration result |
|-----------------|------------------|
| `repo:v1.2.3@sha256:…` | `repo:v1.2.3` (trusts the embedded semver tag, strips the digest) |
| `repo@sha256:…` / `repo:latest@sha256:…` | reverse-lookup: the **highest semver tag whose manifest digest equals the pinned one** → `repo:vX.Y.Z` |

The conversion is faithful — the same image, now expressed as a version — and is
committed + pushed to Git like any other bump (`chore(image): web sha256:… ->
v1.2.0`). The next tick resumes normal semver selection from that tag. If no
published semver tag points at the pinned digest, migration is a no-op and logs
the reason (best-effort, never blocks reconcile). `digest` mode itself is
unaffected: it keeps pinning a mutable tag's digest and is not migrated.


## Artifact provenance at apply time

A signature and provenance answer different questions, and RollOps can now
require both — plus the source gate's own verdict.

`cosign verify` proves somebody holding the key vouched for these bytes. It
says nothing about where they came from — an attacker who obtains the signing
key can sign an arbitrary image, and a signature-only gate deploys it.
Provenance pins the artifact to a source commit and to the platform that built
it, which the same stolen key no longer satisfies.

```bash
ROLLOPS_COSIGN_KEY=/etc/rollops/cosign.pub
ROLLOPS_PROVENANCE_BUILDERS=https://github.com/klarlabs-studio/kiln@
ROLLOPS_SOURCE_GATES=https://warden.klarlabs.de
```

Three claims by three authorities, each checkable on its own:

| Claim | Who says it | Attestation |
|---|---|---|
| somebody vouched for these bytes | whoever holds the key | the signature |
| this artifact was built from commit C | the build platform | SLSA build provenance |
| commit C passed its policy | the source gate | SLSA verification summary |

The third is the source gate's own statement, carried onto the artifact rather
than summarised by the builder — so the verdict names its verifier, the policy
file it was measured against, and the levels it reached, instead of asking you
to take a build tool's word about a gate.

**The commit joins them, and the join is checked.** A verification summary for
some well-gated commit attached to an artifact built from an ungated one
verifies perfectly as two separate attestations; only comparing the commit in
the provenance against the commit in the summary catches it. RollOps does that
whenever both are configured, and says "not joined to the build" in the deploy
log when it cannot.

Both are opt-in and compose. Setting only the key keeps the previous behaviour
exactly; the startup log says which checks are in force.

| Variable | Effect |
|---|---|
| `ROLLOPS_COSIGN_KEY` | public key for keyed verification |
| `ROLLOPS_COSIGN_IDENTITY` / `ROLLOPS_COSIGN_ISSUER` | keyless verification |
| `ROLLOPS_PROVENANCE_BUILDERS` | comma-separated builder ids that may vouch for a deployable artifact |
| `ROLLOPS_PROVENANCE_REQUIRE_REPROVED` | reject artifacts whose source checks were inherited rather than run |
| `ROLLOPS_SOURCE_GATES` | comma-separated source-gate verifier ids whose summary is acceptable |
| `ROLLOPS_SOURCE_REQUIRE_LEVELS` | verification levels the summary must claim, e.g. `WARDEN_SOURCE_SIGNED` |

A trailing `@` matches any version, so `…/kiln@` accepts every release without
a policy change on each one.

### What is checked

RollOps reads the SLSA predicates itself rather than importing types from any
build tool: the formats are standards, and a CD system that could only verify
one builder's output would be the wrong shape. It works with kiln, with
GitHub's SLSA generator, or anything else emitting the spec.

- the attestations are authenticated by the configured key or identity
- `runDetails.builder.id` is in the allowed list — this matters, because cosign
  proves a trusted key signed *an* attestation, not what it says
- the provenance names a source commit
- the summary's verifier is an allowed gate, its verdict is `PASSED`, and it
  reaches any required levels
- the commit in the summary is the commit in the provenance

An image commonly carries several attestations — an SBOM beside its provenance,
a second from a rebuild, one left by an older tool version. Any single
statement satisfying the policy is enough; requiring all of them would let one
stale entry block every deploy of an otherwise good artifact.

Setting builders or gates with no key or identity is refused at startup rather
than accepted: a verifier with nothing to authenticate against would fail every
deploy, which reads as a broken pipeline rather than the misconfiguration it is.
