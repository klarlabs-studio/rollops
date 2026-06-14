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
