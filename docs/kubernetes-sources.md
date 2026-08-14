# Kubernetes Manifest Sources

The Kubernetes target renders its desired state from one of several sources, so
existing Helm / Kustomize / OCI users adopt Rollops unchanged. The Git source is
the daemon's own desired-state poll; these sources describe *what* a target
renders once a sync fires. Set exactly one in the target spec.

Two ways to declare the source:

- **`manifestFrom`** (recommended) — a *referenced* source: a file, Kustomize
  overlay, or Helm chart tracked in the repo, resolved relative to the config
  file's own directory. Preferred for Kustomize/Helm users: no drifting inline
  copy, and drift keys off the rendered output (see below). See
  [manifestFrom](#manifestfrom).
- **Flat keys** (`helm` / `kustomize` / `oci` / `bucket` / `manifest`) — the
  original form. **Legacy but retained**: kept working for backward compatibility
  and not slated for removal, but new configs should prefer `manifestFrom` (it
  resolves relative to the config dir and keys drift off the rendered output).
  Precedence when no `manifestFrom` is set: `oci` > `helm` > `kustomize` >
  `manifest`.

`manifestFrom` is exclusive: it may not be combined with an inline `manifest` or
any flat key. Config load rejects a target that sets more than one source, or a
`manifestFrom` with more than one of `path` / `kustomize` / `helm`.

## manifestFrom

Render the desired manifest from a source **referenced** in the repo instead of
inlining a copy. Exactly one of `path`, `kustomize`, or `helm`:

```yaml
target:
  kind: kubernetes
  spec:
    namespace: myapp
    resource: deployment/api
    manifestFrom:
      path: k8s/api.deployment.yaml        # a single file
      # kustomize: k8s/overlays/prod        # a kustomize dir (or remote URL)
      # helm: { chart: ./charts/api, values: [values-prod.yaml] }
```

**Path resolution.** Relative paths resolve against the directory of the config
file this target came from — the Git checkout for the daemon, the config file's
directory for the CLI — *never* the daemon's working directory. The daemon and
one-shot CLI behave identically. Absolute paths and `..` escapes are rejected;
remote Kustomize/Helm sources (a URL, an `oci://` ref, a `git@` ref, or a
`repo//path` marker) pass through untouched. Rendering runs at safe defaults:
kustomize with no alpha/exec plugins, `helm template` with no post-renderer.

- **path** — read a single manifest file, applied verbatim.
- **kustomize** — a string: a local overlay directory (rooted + confined) or a
  remote URL, built with `kubectl kustomize`.
- **helm** — a mapping: `chart` (a local dir, rooted + confined, or a remote
  chart name with `repo`, or an `oci://` ref), optional `repo` / `version` /
  `namespace` / `releaseName`, and `values` as a list of values **files**
  (each resolved against the root). Rendered with `helm template`.

**Drift.** For a referenced source the drift checksum is computed over the
**rendered output**, so an edit to a referenced Kustomize/Helm/path file is
detected as drift and reconciled even under `shallow` verification. Inline
`manifest` and the flat keys keep their spec-derived checksum. Preview the
resolved manifest with `rollops plan <config.yaml>` — it prints the rendered
result under `--- rendered manifest ---`.

Run `rollops doctor <config.yaml>` to confirm the render tools a target needs
(`kubectl` always; `helm` when a Helm source is used) are present.

## helm

Render a Helm chart with `helm template`. The chart may come from an HTTP Helm
repository or an OCI registry:

```yaml
target:
  kind: kubernetes
  spec:
    helm:
      chart: nginx
      repo: https://charts.bitnami.com/bitnami   # HTTP Helm repo
      version: "15.0.0"
      releaseName: web
      namespace: web
      values:
        replicaCount: 3
```

For an **OCI Helm chart**, set `chart` to the `oci://` reference and omit `repo`:

```yaml
    helm:
      chart: oci://ghcr.io/acme/charts/app
      version: "1.4.2"
      values: { image: { tag: v1.4.2 } }
```

Values are passed to `helm template` over stdin.

## kustomize

Build a Kustomize overlay. The path may be local or a remote URL (a Git URL is
fetched by kubectl):

```yaml
    kustomize:
      path: github.com/acme/config//overlays/prod
```

## oci

Pull a non-Helm **OCI artifact** — a bundle of manifests or a kustomize tree, the
Flux `OCIRepository` model — with `oras pull`, then render its contents. This
lets desired state live in an OCI registry instead of a Git checkout:

```yaml
    oci:
      ref: oci://ghcr.io/acme/app-manifests:v1.2.3
      path: overlays/prod        # optional subdir within the artifact
      render: kustomize          # kustomize (default) | manifest
      # file: deploy.yaml        # required when render: manifest
```

- `render: kustomize` (default) runs `kubectl kustomize` over the artifact
  (subdir `path` if given).
- `render: manifest` reads a single `file` from the artifact and applies it
  verbatim.

Requires `oras` and `kubectl` on the daemon host. The artifact is pulled to a
temp directory and removed after rendering.

## bucket

Sync desired state from an object-storage bucket (the Flux `Bucket` source),
then render it like `oci`. The URL scheme selects the CLI: `s3://` uses
`aws s3 sync`, `gs://` uses `gsutil -m rsync -r`. Credentials are the CLI's
ambient resolution.

```yaml
    bucket:
      url: s3://acme-config/prod      # or gs://acme-config/prod
      path: overlays/prod             # optional subdir
      render: kustomize               # kustomize (default) | manifest
      # file: deploy.yaml             # required when render: manifest
```

Requires `aws` (for `s3://`) or `gsutil` (for `gs://`) plus `kubectl` on the
daemon host. The bucket is synced to a temp directory and removed after
rendering.

## manifest

Inline YAML, applied verbatim:

```yaml
    manifest: |
      apiVersion: apps/v1
      kind: Deployment
      ...
```

## Health assessment (CRDs)

Standard workloads (Deployment, StatefulSet, DaemonSet) report readiness via
`kubectl rollout status`. Any other kind — a CRD such as a cert-manager
`Certificate`, an Argo `Rollout`, a Crossplane resource — is assessed from its
`status.conditions`, the way Argo CD's health checks do: the target is healthy
when its `Ready` / `Available` / `Succeeded` condition is `True`, unhealthy (with
the condition's reason + message) when `False`, and still progressing for any
other value. A resource with no conditions is treated as healthy.

Pin a specific condition type for CRDs that use a non-standard one:

```yaml
target:
  kind: kubernetes
  spec:
    resource: certificates.cert-manager.io/app-cert
    healthCondition: Ready        # gate on this condition type
```

## Drift verification depth

By default Rollops detects drift with a stamped-checksum marker (`shallow`):
cheap, but an out-of-band field edit that leaves the marker intact — e.g.
`kubectl set image` — is not seen. Set `verification: full` at the spec level to
also diff live state against the desired manifest, so any divergence is surfaced
as drift:

```yaml
spec:
  verification: full   # shallow (default) | full
  target:
    kind: kubernetes
    spec: { resource: deployment/api, namespace: prod }
```

`full` runs a `kubectl diff` when the marker says "in sync"; a non-empty diff is
reported as an update (`live drifted from desired …`). Use it where out-of-band
changes must be caught at the cost of a diff per plan.

## ignoreDifferences

Kubernetes controllers (HPA, webhooks) write fields that are not in Git. List
JSON pointers or dotted paths to ignore in the live Diff so those writes are
not drift. Apply stays `kubectl apply`; this only changes whether a diff counts.

```yaml
spec:
  verification: detect
  target:
    kind: kubernetes
    spec:
      resource: deployment/web
      namespace: prod
      ignoreDifferences:
        - /spec/replicas
        - spec.template.spec.containers.0.resources
```

An ignored-only difference is not drift. Any other field still is. The checksum
stamp (Observe) is unchanged.
