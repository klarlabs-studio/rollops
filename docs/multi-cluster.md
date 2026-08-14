# Multi-Cluster

One Rollops daemon can drive rollouts across many Kubernetes clusters. There is
no central cluster registry to operate: each target selects its cluster through
standard kubectl credentials, so multi-cluster is just per-target configuration.

## Selecting a cluster per target

A Kubernetes target picks its cluster with `context` (a kubeconfig context) and,
optionally, `kubeconfig` (a credentials file specific to that cluster):

```yaml
# team-a/prod-east.yaml
spec:
  target:
    kind: kubernetes
    ref: team-a/prod-east/api
    spec:
      kubeconfig: /etc/rollops/clusters/prod-east.kubeconfig
      context: prod-east
      namespace: web
      resource: deployment/api
```

```yaml
# team-a/prod-eu.yaml
spec:
  target:
    kind: kubernetes
    ref: team-a/prod-eu/api
    spec:
      kubeconfig: /etc/rollops/clusters/prod-eu.kubeconfig
      context: prod-eu
      namespace: web
      resource: deployment/api
```

- `kubeconfig` lets a cluster's credentials live in its own file (a mounted
  secret per cluster), so the daemon manages clusters it has no ambient access
  to. Omit it to use the ambient `KUBECONFIG` / in-cluster config.
- `context` selects a context within whichever kubeconfig applies.

Each target's reconcile, drift detection, progressive steps, health gates, and
rollback run independently against its own cluster. Cross-cluster ordering uses
the same `dependsOn` and environment-promotion mechanics as any other targets.

## RolloutSet list generator

To avoid N copies of the same config, commit one `kind: RolloutSet` with a
**list** generator. The watcher expands it in memory into ordinary
`RolloutConfig`s (Git holds the template; generated configs are not written
back). Each element must produce a unique `target.ref`. Cluster and matrix
generators are not in this build.

```yaml
apiVersion: rollops.klarlabs.de/v1
kind: RolloutSet
metadata: { name: web }
generators:
  - list:
      elements:
        - { name: east, kubeconfig: /etc/rollops/east, context: east }
        - { name: west, kubeconfig: /etc/rollops/west, context: west }
template:
  spec:
    target:
      kind: kubernetes
      ref: "web@{{name}}"
      criticality: low
      spec:
        kubeconfig: "{{kubeconfig}}"
        context: "{{context}}"
        namespace: web
        resource: deployment/web
        manifestFrom: { path: deploy/web.yaml }
    strategy: { type: rolling }
```

## Fleet view

This is per-target, infrastructure-agnostic multi-cluster — deliberately leaner
than a cluster-registry control plane. Aggregating many daemons into one fleet
dashboard (with RBAC, audit, and cross-tenant views) is the job of the
commercial Studio layer; the OSS daemon stays a single, self-contained control
loop over the clusters its targets name.
