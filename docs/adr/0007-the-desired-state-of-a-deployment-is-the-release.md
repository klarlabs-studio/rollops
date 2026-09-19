# ADR-0007 — The desired state handed to a target is the release, and nothing else

- **Status:** accepted
- **Date:** 2026-09-19
- **Spec:** `docs/architecture/rollops-next.md` §8.2, §8.3, §9, §24 · **Backlog:** R7, R11
- **Invariants touched:** INV-007 (infrastructure independence), INV-012 (secret non-persistence)
- **Decides:** a type the spec uses but never defines

## Context

`targetv2.DesiredState` is the argument every target method takes, and §9 gives
it a shape — `Kind`, `Spec`, `Checksum`, `Labels`, `Rendered` — without saying
what goes in it. Nothing in the spec turns a `Release` into one. The planner
(`internal/engine/planner`) therefore has a `Desired` port with no
implementation, and until there is one, `deploy.Service` cannot be constructed.

Four things constrain what may go in the document.

**It crosses a process boundary.** §33.3 puts out-of-process plugins behind a
versioned RPC protocol, so the document is wire format, not a Go value. Its
field names are as much contract as the method names around it.

**A target may be written by someone else, in another language, on its own
release cycle.** A field added to the document has to be ignorable by a target
that predates it, and a change that is not ignorable has to be visible as a
different thing rather than silently misread.

**The checksum is load-bearing.** A target compares it to decide whether it has
already converged. A checksum that moves for a reason that is not a change to
what is being deployed makes every plan report work that does not exist — and
§8.2 forbids planning from having side effects, so the plan is the only place
that mistake shows up.

**RollOps does not know what a deployment looks like.** INV-007 says so, and it
is the reason the planner dispatches rather than plans. Whatever the document
says, it cannot say it in Kubernetes', Helm's or Compose's vocabulary.

The awkward question is configuration. An environment's `TargetBinding` carries
`Config map[string]value.Ref` — the namespace, the cluster, the credential. Two
plausible readings: the desired state is "this release, configured this way",
or the desired state is "this release" and the configuration is the target's
own.

## Decision

**The document is `rollops.release/v1`: the release identity, its source
revision, and its artifacts pinned by digest and sorted by role. No binding
configuration goes in it, literal or secret.**

```json
{
  "kind": "rollops.release/v1",
  "release": {
    "id": "rel_…",
    "version": "2.0.0",
    "source": { "provider": "github", "repository": "example/app", "revision": "0d3c1f4…" }
  },
  "artifacts": [
    {
      "role": "app",
      "kind": "oci-image",
      "digest": "sha256:…",
      "locator": "ghcr.io/example/app@sha256:…",
      "mediaType": "application/vnd.oci.image.manifest.v1+json"
    }
  ]
}
```

It is rendered by `internal/engine/desired`, which is the planner's `Desired`
port.

Four consequences of that shape are deliberate.

**`Kind` is versioned.** A target reads `rollops.release/v1` and knows what it
is holding. Adding a field keeps the kind; anything a v1 reader would
misinterpret gets `v2`, so an old target refuses rather than guesses.

**Artifacts are sorted by role and the checksum is `digest.Of(Spec)`.** A
release lists its artifacts in whatever order they were declared. Sorting makes
two renders of one release byte-identical, which is what lets the checksum mean
"the same thing" rather than "the same declaration order".

**The configuration stays out.** The target was built from that same binding by
`internal/engine/targets` and already holds it. Copying it into the document
would put a resolved credential into bytes that are checksummed, persisted with
the plan, and handed to a subprocess that may log its input — which is exactly
what INV-012 forbids. It would also make the checksum of one release differ per
environment, so promoting a release from staging to production would look like
deploying a different thing.

A configuration change still surfaces. The target compares the document against
its own live state and reports `Changes`, because its configuration changed even
though the document did not.

**`Rendered` is nil.** §9 offers it for a desired state that pointed at
something external and had it resolved. This one points at nothing: it is the
whole of what RollOps knows, and the target supplies the rest from its own
configuration. A target whose spec is a chart repository fills `Rendered` on the
way back, through `PlanResult.RenderedChecksum`.

The renderer resolves each artifact id through `port.ArtifactRepository` and
refuses one whose `ProjectID` differs from the release's. Artifacts are looked
up by id alone, so without that check a release could name an image its project
was never allowed to see.

## Consequences

- `deploy.Service` is constructible. Both planner ports now have
  implementations: `internal/engine/targets` and `internal/engine/desired`.
- Every target must know how to turn a release into its own substrate's
  manifests. That work moves out of RollOps and into the target, which is where
  INV-007 wants it, and it means a target cannot be a thin shell around a CLI
  without an opinion about packaging.
- The same release rendered for staging and for production has the same
  checksum. That is what makes promotion verifiable: §8 can assert the artifact
  that passed staging is the artifact production receives, by comparing one
  string.
- A field added to the document is a change every target may have to notice. The
  version in `Kind` is the only lever, and it is coarse — a v2 is a flag day for
  third-party targets. The document is therefore kept small on purpose.
- Configuration drift is invisible to the plan hash. Two plans against the same
  release with different binding configuration hash identically at the
  desired-state layer; what distinguishes them is the `PlannedOperation` the
  target produced. An approval binds to the plan, not to the document, so §13's
  binding rule is unaffected.

## Alternatives rejected

**Put the binding configuration in the document.** It reads naturally — the
desired state is everything needed to converge — and it is how a Helm values
file works. Rejected on INV-012: `value.Ref` exists so that secret material is
resolved as late as possible and never persisted, and the document is persisted
by way of the checksum, logged by way of the plugin, and read by way of the
plan. It also breaks promotion equivalence, which is the more permanent cost.

**Let the document carry an opaque provider payload.** §8.3 explicitly permits
one: "provider-specific payload MAY be attached as a versioned opaque field, so
long as the core summary/diff remains portable." Declined for the same reason
the planner declines the target's free-form diff — an opaque blob is a blob
nobody can redact, and `plan.Redacted()` can only strip *structured* changes
marked sensitive. The permission stays available if a substrate later needs
something this document cannot express; taking it now would mean taking it
before there is a case for it.

**Render substrate manifests in RollOps.** A `kubernetes` renderer here would
make targets thin. It is the straight road to a Kubernetes-shaped control plane
with adapters bolted on, which INV-007 exists to prevent.

**Key artifacts by index rather than role.** Simpler, and the release already
holds them in order. Rejected because a target has to know which artifact is the
web image and which is the migration job, and position is not a name — inserting
an artifact would silently re-point every target that read past it.
