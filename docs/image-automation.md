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
