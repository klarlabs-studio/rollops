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
rollback run independently against its own cluster. Cross-cluster (and
in-repo) ordering: if B `dependsOn` A, reconcile skips B this tick until A is
promoted — logged, not fatal to the rest of the repo.

## RolloutSet list generator

To avoid N copies of the same config, commit one `kind: RolloutSet` with a
**list** or **cluster** generator. The watcher expands it in memory into ordinary
`RolloutConfig`s (Git holds the template; generated configs are not written
back). Each element must produce a unique `target.ref`. Matrix generators are
not in this build.

### List generator

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

### Cluster generator

Point `ROLLOPS_CLUSTERS` at a registry file the daemon loads at boot:

```yaml
# /etc/rollops/clusters.yaml
clusters:
  - { name: east, kubeconfig: /etc/rollops/clusters/east, context: east, labels: { tier: prod, env: prod } }
  - { name: west, kubeconfig: /etc/rollops/clusters/west, context: west, labels: { tier: prod, env: staging } }
  - { name: dev,  kubeconfig: /etc/rollops/clusters/dev,  context: dev,  labels: { tier: dev } }
```

```yaml
kind: RolloutSet
metadata: { name: web }
generators:
  - cluster:
      selector: { matchLabels: { tier: prod } }   # empty selector → all clusters
template:
  spec:
    target:
      kind: kubernetes
      ref: "web@{{cluster.name}}"
      env: "{{cluster.labels.env}}"
      criticality: low
      spec:
        kubeconfig: "{{cluster.kubeconfig}}"
        context: "{{cluster.context}}"
        namespace: web
        resource: deployment/web
        manifestFrom: { path: deploy/web.yaml }
    strategy: { type: rolling }
```

Placeholders: `name`, `kubeconfig`, `context`, `cluster.name`,
`cluster.kubeconfig`, `cluster.context`, and `cluster.labels.<key>`.

## Fleet view

This is per-target, infrastructure-agnostic multi-cluster — deliberately leaner
than a cluster-registry control plane. Aggregating many daemons into one fleet
dashboard (with RBAC, audit, and cross-tenant views) is the job of the
commercial Studio layer; the OSS daemon stays a single, self-contained control
loop over the clusters its targets name.
