# RFC: Multi-cluster at scale

Status: **Draft / scoping** — not yet approved or scheduled.
Date: 2026-06-14.

## Problem

Rollops already deploys to multiple clusters: a Kubernetes target sets its own
`spec.kubeconfig` + `context`, so one daemon drives many clusters, each with its
own credentials, with no central registry (`internal/target/kubernetes/cluster_kubectl.go`).
That works fine for a handful of targets.

It does **not** scale to "service X across N clusters." Today that means N
hand-written `RolloutConfig`s (or N copies under a repo directory), each
identical except for `kubeconfig`/`context`/`env`. Adding a cluster, or rolling
a change to all of them, is O(N) manual edits. This is the toil ArgoCD's
**ApplicationSets** remove via generators + a template.

Goal: deploy/define a rollout once and have it apply across a set of clusters
(or environments), without per-cluster boilerplate, while keeping Rollops's
"Git is the source of truth" and per-service reconcile model.

## What competitors do

- **ArgoCD ApplicationSets** — a controller that templates `Application` CRs from
  *generators*: `list`, `cluster` (from registered cluster secrets), `git`
  (directories/files), `matrix`, `merge`. The generated Applications are real
  objects reconciled independently.
- **Flux** — no direct equivalent; teams use Kustomize overlays + variable
  substitution, or per-cluster `Kustomization` objects, often generated in CI.

The ArgoCD model is the bar. The open question is which parts fit Rollops.

## Does it fit Rollops?

Rollops is **not** a Kubernetes controller — it has no CRDs and generates no
in-cluster objects. Its unit is a `RolloutConfig` loaded from Git and reconciled
independently (`internal/reconcile` watches N repos; a repo directory already
loads every `*.yaml`). Per-config rollout **state, history, drift, RBAC scope,
and risk gate** all key on the config's `target.ref`.

That gives a natural fit: **expand one templated definition into N ordinary
configs**, and let the existing per-config machinery handle each. We do *not*
need (and should avoid) an Application-CR-style controller or a parallel state
model. The expansion is the only new concept.

Tension to respect: Git-is-truth. Two ways to expand —
1. **In-memory at load time** — the watcher expands a `RolloutSet` template into
   N `NamedConfig`s before reconciling. Git holds the *template*; generated
   configs are ephemeral. Reuses all per-config machinery.
2. **Generate commits into Git** — like image-automation writeback. Auditable,
   but heavier, and couples generation to push access.

Recommendation leans to (1): smallest blast radius, no new state, no Git writes.

## Options

### Option A — Cluster registry + in-engine fan-out
One config; the engine reconciles it against every matching cluster. Requires
per-(config × cluster) state keying, per-cluster status/rollback/risk — a deep
change to the store and engine. **Rejected for v1**: large blast radius, parallel
state model, exactly the controller-shaped complexity we want to avoid.

### Option B — `RolloutSet` generator → N configs (recommended)
A new `kind: RolloutSet` with *generators* + a `template` (a `RolloutConfig`
with placeholders). The watcher expands it in memory into N configs, each a
normal `RolloutConfig` with a distinct, deterministic `target.ref` (e.g.
`web@us-east-1`) and per-item `kubeconfig`/`context`/`env`. Each reconciles,
stores state, rolls back, and authorizes independently — **zero engine/store
change**; the work is load-time expansion + a cluster registry for the `cluster`
generator.

```yaml
apiVersion: rollops.klarlabs.de/v1
kind: RolloutSet
metadata: { name: web }
generators:
  - cluster: { selector: { matchLabels: { tier: prod } } }   # from the registry
template:
  spec:
    target:
      kind: kubernetes
      ref: "web@{{cluster.name}}"
      env: "{{cluster.labels.env}}"
      spec:
        kubeconfig: "{{cluster.kubeconfig}}"
        context: "{{cluster.context}}"
        namespace: web
        resource: deployment/web
    strategy: { type: canary }
```

Cluster registry (`ROLLOPS_CLUSTERS` → a file, mounted Secret/ConfigMap):

```yaml
clusters:
  - { name: us-east-1, kubeconfig: /etc/rollops/clusters/use1, context: use1, labels: { tier: prod, env: prod } }
  - { name: eu-west-1, kubeconfig: /etc/rollops/clusters/euw1, context: euw1, labels: { tier: prod, env: prod } }
```

### Option C — External generation (no Rollops feature)
Document using Kustomize/Helm/CI to render the N per-cluster configs into the
repo. Zero code; toil moves to CI; no native cluster discovery. Viable as the
*interim* answer and as the escape hatch for exotic generation.

## Recommendation

**Ship Option C as guidance now** (it already works), and **build Option B in
phases**:

- **Phase 1** — Cluster registry (`name`/`kubeconfig`/`context`/`labels`) +
  `RolloutSet` with a `list` generator + template expansion in the watcher.
  Deterministic per-item `ref`. Validation: every generated config must validate;
  `ref` uniqueness enforced.
- **Phase 2** — `cluster` generator (iterate the registry, label selector). This
  is the ApplicationSet headline.
- **Phase 3** — `matrix`/`git-directory` generators; a fleet status rollup
  ("web: 9/10 clusters promoted") in CLI/UI; optional promotion *waves*
  (sequential cluster groups), reusing the existing `Environments` concept.

## Risks & non-goals

- **Identity/keying** — generated `ref` must be deterministic and unique, or
  store/history/drift collide. Validate at expansion.
- **Blast radius** — a template typo becomes N bad rollouts. Mitigated by the
  per-config risk gate + approval floor still firing per generated config; a
  `plan` over a `RolloutSet` should preview all N.
- **Partial failure** — one cluster failing must not block the rest. The
  existing independent-per-config reconcile already gives this (same as multi-repo
  watch).
- **Secrets** — per-cluster kubeconfigs live in the registry as mounted Secrets,
  same trust model as today.
- **Non-goals** — cluster *provisioning* (that's Cluster API), cross-cluster
  traffic, and any in-cluster CR generation. Rollops expands configs; it does not
  become a controller.

## Open questions

1. In-memory expansion vs generate-into-Git (recommend in-memory; revisit if
   operators want the generated configs to be reviewable in Git).
2. Cluster registry delivery: flat file (Phase 1) vs a future read API.
3. Promotion across clusters: independent (Phase 1–2) vs ordered waves (Phase 3)
   — how it composes with `spec.environments`.
4. Templating engine: minimal `{{...}}` substitution vs a real templating lib
   (prefer minimal, no new dep, consistent with the rest of the codebase).

## Estimate

Phase 1 is a contained, multi-file feature (config kind + registry loader +
watcher expansion + validation + tests), roughly the size of the GitHub-App or
RBAC-policy-file PRs. Phases 2–3 are separate PRs. No store/engine schema change
in any phase — that is the design's main virtue.
