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
