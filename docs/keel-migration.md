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

## Moving an app from digest to semver

An app whose CI only publishes `:latest` (+ commit SHA) starts on `digest` mode —
it auto-updates on every push, preserving the pre-Rollops behaviour. To switch
to versioned releases:

1. Make CI publish a semver image on a release tag (additive — keep
   `:latest` + SHA on main). Example with `docker/metadata-action`:

   ```yaml
   on: { push: { tags: ['site-v*.*.*'] } }   # plus the existing branch trigger
   - id: meta
     uses: docker/metadata-action@v5
     with:
       images: ghcr.io/acme/site
       tags: |
         type=match,pattern=site-v(\d+\.\d+\.\d+),group=1
         type=match,pattern=site-v(\d+\.\d+),group=1
   - uses: docker/build-push-action@v6
     with: { tags: "${{ steps.meta.outputs.tags }}" }
   ```

2. Cut the first release: `git tag site-v0.1.0 && git push origin site-v0.1.0`.

3. Flip the app's `.rollops` config to track it:

   ```yaml
   spec:
     target: { spec: { image: ghcr.io/acme/site:0.1.0 } }   # was :latest
     imagePolicy: { mode: minor }                            # was digest
   ```

From then on Rollops adopts each new `site-vX.(Y+1).Z` automatically and commits
the bump back to the app repo.
