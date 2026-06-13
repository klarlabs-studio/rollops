# Migrating from keel to Rollops

Replacing keel (registry-watch image auto-update) with Rollops GitOps + image
automation. Config lives in each app's own repo under `.rollops/`; rollopsd
watches them, reconciles desired state, and pushes image bumps back.

## 1. Add a config to each app repo

For each keel-managed app, capture its live deployment faithfully and commit a
`RolloutConfig` to its repo. Verify the captured manifest matches live before
anything applies:

```sh
kubectl -n <ns> get deploy <name> -o yaml --show-managed-fields=false > live.yaml
# strip status/managedFields/keel annotations → manifest
kubectl diff -f manifest.yaml    # MUST be metadata-only (no spec/template change)
```

Wrap it in `.rollops/rollops.yaml` (directory; many apps → many files):

```yaml
spec:
  target:
    kind: kubernetes
    spec:
      namespace: <ns>
      resource: deployment/<name>
      image: ghcr.io/acme/<name>:latest   # tracked image
      manifest: |
        <the faithful live manifest>
  strategy: { type: rolling }
  imagePolicy:
    mode: digest            # :latest → digest; semver tags → minor/patch/major
    allowMutableTags: true
```

## 2. Point rollopsd at the repos

`ROLLOPS_WATCH` lists every app repo (path `.rollops`, `tokenFile` for private).
Enable image automation: `ROLLOPS_IMAGE_AUTOMATION=1` plus
`ROLLOPS_REGISTRY_USER` / `ROLLOPS_REGISTRY_TOKEN` for private registries. The
git token needs Contents **read+write** (automation pushes bumps).

```sh
kubectl -n rollops-system create secret generic rollopsd-git --from-literal=token=<scoped-pat>
kubectl -n rollops-system create secret generic rollopsd-registry --from-literal=user=<u> --from-literal=token=<scoped-pat>
kubectl -n rollops-system patch deploy rollopsd --patch-file deploy/kubernetes/cutover-patch.yaml
kubectl -n rollops-system rollout status deploy/rollopsd
```

## 3. Verify, then retire keel

Watch the reconcile logs apply each app (creating/stamping, removing keel
annotations — a metadata-only change, no pod restart). Confirm the sites are
healthy and image automation pins digests:

```sh
kubectl -n rollops-system logs deploy/rollopsd | grep -E 'reconcile|image automation'
kubectl -n <ns> get deploy <name>   # still Ready
```

Retire keel — drop its policy annotations so it stops managing the apps, then
remove it:

```sh
# per app (stops keel touching it)
kubectl -n <ns> annotate deploy <name> keel.sh/policy- keel.sh/trigger- keel.sh/pollSchedule-
# then remove keel entirely
kubectl delete ns keel
```

Rollback is just re-adding the keel annotations and scaling rollopsd to zero —
nothing in the apps changed, so there is no data to recover.
