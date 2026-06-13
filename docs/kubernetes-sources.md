# Kubernetes Manifest Sources

The Kubernetes target renders its desired state from one of several sources, so
existing Helm / Kustomize / OCI users adopt Rollops unchanged. The Git source is
the daemon's own desired-state poll; these sources describe *what* a target
renders once a sync fires. Set exactly one in the target spec.

Precedence: `oci` > `helm` > `kustomize` > `manifest`.

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
