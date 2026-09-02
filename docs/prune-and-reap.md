# Prune and orphan reclamation

Two related but separate behaviours keep a Kubernetes target's live resources
aligned with Git. Confusing them is how a retired service keeps running with
nothing in the repository describing it (#154).

## Within a live target: `prune`

When a `RolloutConfig` still exists and is reconciled, `prune: true` makes each
apply garbage-collect resources that used to be in that target's manifest set
but are no longer declared:

```yaml
spec:
  target:
    kind: kubernetes
    ref: payments/prod/api
    spec:
      namespace: payments
      resource: deployment/api
      prune: true
      # ...
```

Rollops labels every applied resource with `rollops.klarlabs.de/target=<ref>`
on **every** apply (whether or not prune is set). Prune only adds
`kubectl apply --prune --selector …`. Labelling is identity; prune is the
destructive behaviour built on top of it.

## When the RolloutConfig itself is deleted: orphans

`--prune` cannot cover deleting the declaration. Pruning runs as part of
applying a target, so it needs the target to still exist. Deleting the
`RolloutConfig` removes the thing that would clean up after it.

The daemon compares one tick's config set against the previous tick. After a
target has been absent for enough consecutive successful loads (~10 ticks),
rollops:

1. **Always reports** the orphan (log + audit), naming the target label so you
   can find leftovers with
   `kubectl get all -l rollops.klarlabs.de/target=<value>`.
2. **Deletes only when opted in** via `reapOnDelete: true` — a separate flag
   from `prune`. `prune: true` alone does **not** reclaim on file removal.

```yaml
spec:
  target:
    kind: kubernetes
    ref: payments/prod/api
    spec:
      namespace: payments
      resource: deployment/api
      prune: true          # GC within a live apply set
      reapOnDelete: true   # remove when this RolloutConfig disappears
      # Optional. Default is kubectl's `all` (deployments, services, pods, …).
      # It excludes ingress, configmap, secret, pvc, serviceaccount, CRDs.
      # Widen explicitly when the target manages those kinds:
      # reapTypes: [all, ingress, configmap]
```

## Safety notes

- Accidental file deletion should not nuke production by default — that is why
  reclamation is opt-in.
- A load/clone error **resets** absence progress; an outage must not retire
  services that were present every time the repo actually loaded.
- A reap that matches nothing is reported as a warning (default `reapTypes`
  may exclude the kinds you manage), not as a successful cleanup.
- Resources applied before the always-label change, or never applied at all,
  may lack the target label and need a manual cluster check.
- Orphan tracking is in-memory for the daemon process: a config deleted while
  the daemon is down is not observed as an absence after restart.

## See also

- Kubernetes target sources: [`docs/kubernetes-sources.md`](kubernetes-sources.md)
- Deploying the daemon: [`docs/deploy-kubernetes.md`](deploy-kubernetes.md)
