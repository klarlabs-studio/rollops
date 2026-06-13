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

## Fleet view

This is per-target, infrastructure-agnostic multi-cluster — deliberately leaner
than a cluster-registry control plane. Aggregating many daemons into one fleet
dashboard (with RBAC, audit, and cross-tenant views) is the job of the
commercial Studio layer; the OSS daemon stays a single, self-contained control
loop over the clusters its targets name.
