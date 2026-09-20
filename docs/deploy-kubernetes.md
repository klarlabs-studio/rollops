# Deploy rollopsd on Kubernetes

Run the Rollops daemon in-cluster: it reconciles watched Git repos on an
interval, serves the UI/REST API, and drives rollouts through its
ServiceAccount. This is the GitOps deployment (the systemd path is in
`docs/deploy-systemd.md`).

## Image

`ghcr.io/klarlabs-studio/rollopsd` — a pure-Go build (the UI is embedded) on a
minimal Alpine base carrying `kubectl` and `git`, which the Kubernetes target,
traffic-router plugin, and reconciler shell out to. Build it yourself with the
repo `Dockerfile`:

```sh
docker build --build-arg VERSION=v0.15.0 -t my-registry/rollopsd:v0.15.0 .
docker push my-registry/rollopsd:v0.15.0
```

## Manifests

The install is two files, split along the line of what the daemon may apply to
itself:

- **`deploy/kubernetes/rollopsd-infra.yaml`** — bootstrap: namespace, TLS
  material (cert-manager ClusterIssuer/Issuer/Certificates), ServiceAccount,
  ClusterRole/Binding, PVC, and Service. Applied by a human, once. RBAC grants
  apply/observe on workloads, patch on Gateway API `HTTPRoute`s, and read on
  CRDs (for `status.conditions` health). It is cluster-scoped for simplicity;
  narrow it to per-namespace Roles in stricter setups.
- **`deploy/kubernetes/rollopsd-deployment.yaml`** — the Deployment alone, and
  the only object rollops manages for itself (see *Self-management* below).

The split is not cosmetic. The daemon's ServiceAccount cannot get cert-manager
`ClusterIssuers`, and it must not be able to rewrite the ClusterRole that grants
it everything else — a workload that can widen its own permissions has none.
While the self-managed manifest was the full install file, every reconcile was
refused at the server-side dry run and **nothing was applied**:

```
clusterissuers.cert-manager.io "rollopsd-selfsigned" is forbidden: User
"system:serviceaccount:rollops-system:rollopsd" cannot get resource
"clusterissuers" in API group "cert-manager.io" at the cluster scope
```

The watch ConfigMap is in neither file: re-applying a manifest must never
clobber a running fleet's watch list. Create it once from
`rollopsd-watch.example.yaml`.

## Secrets (out of band — never committed)

```sh
# API + UI auth
kubectl -n rollops-system create secret generic rollopsd-secrets \
  --from-literal=admin-token="$(openssl rand -hex 24)" \
  --from-literal=ui-password="$(openssl rand -hex 12)"

# Image pull (only if the image is private)
kubectl -n rollops-system create secret docker-registry ghcr \
  --docker-server=ghcr.io --docker-username=<user> --docker-password=<token>
```

## What rollopsd watches

The watch ConfigMap lists the repos to reconcile — name, URL, branch, and a path
within the repo holding `rollops.yaml` rollout configs:

```json
[{ "name": "demo", "url": "https://github.com/acme/config", "branch": "main", "path": "deploy" }]
```

`path` may be a single file (`deploy/rollops.yaml`) or a **directory** — a
directory loads every `*.yaml` in it, so one repo path manages many apps.

For a **private** repo, add auth (mount a Secret, never inline the token in the
ConfigMap):

```json
[{ "name": "cluster", "url": "https://github.com/acme/cluster-config",
   "branch": "main", "path": "apps", "tokenFile": "/etc/rollops/git/token" }]
```

`tokenFile` is read at startup and sent as an `Authorization` header (never
written to disk or the remote URL). `deployKeyPath` is the SSH alternative for
`git+ssh` remotes.

`ROLLOPS_WATCH` points at the mounted `watch.json`; `ROLLOPS_RECONCILE_INTERVAL`
sets the poll cadence. A "Sync now" button in the UI triggers an immediate
reconcile.

## rollops deploys rollops

The daemon executes the rollouts; the CLI only asks it to. A client newer than
the daemon therefore describes behaviour that is not in force — which is how a
cluster ran `rollopsd:v0.34.3` while this repository pinned `v0.34.8`, four
releases of rollout fixes that never deployed. Nothing applied the pin.

`rollops.yaml` at the repository root is rollops' own rollout config: it targets
`rollops-system/deployment/rollopsd`, renders
`deploy/kubernetes/rollopsd-deployment.yaml`, and carries an `imagePolicy` that follows the released image. It sits at the
root because a referenced manifest resolves against the **repo checkout root**
for the daemon and against the **config file's own directory** for the CLI,
and `..` is refused — only a root config reads the same way to both. Watch this
repository with `path: rollops.yaml` and the daemon keeps itself current: a release
publishes `ghcr.io/klarlabs-studio/rollopsd:vX.Y.Z`, the daemon notices the new
tag, opens a PR bumping the tracked image (main is protected, so writeback is
`pull-request`), and the merge deploys it on the next reconcile.

Two things follow from that:

- **The manifest must be the whole desired state of what it manages.**
  Self-management applies `deploy/kubernetes/rollopsd-deployment.yaml`, so
  anything set by hand on the live Deployment and missing from that file is
  withdrawn on the next apply. Bootstrap objects are outside that loop, so a
  hand-granted RBAC rule survives — but it also drifts silently from the
  repository, which is why the Prometheus-operator rules were written back into
  `rollopsd-infra.yaml` rather than left on the cluster.
- **A hand-apply strips the target label.** rollops stamps
  `rollops.klarlabs.de/target` onto what it applies, so the live Deployment
  carries a label the manifest does not, and `kubectl apply -f` removes it. The
  next reconcile puts it back; if you are applying by hand *instead of*
  reconciling, re-add it.
- **Check the skew when something looks wrong.** `rollops doctor` reports the
  daemon's version beside the client's and **fails** when they differ; every
  daemon-mode command prints a one-line warning after it runs. A daemon older
  than the version handshake reports no version, and doctor says so rather than
  claiming a match.

## Apply

```sh
kubectl apply -f deploy/kubernetes/rollopsd-infra.yaml \
              -f deploy/kubernetes/rollopsd-deployment.yaml
kubectl -n rollops-system rollout status deploy/rollopsd
kubectl -n rollops-system port-forward svc/rollopsd 8080:80   # open http://localhost:8080
```

Probes: `/readyz` means the process is up; `/livez` means the cgroup still has
process-slot headroom. Liveness uses `/livez` so a PID leak that fills
`pids.max` (the v0.34.3 class of failure) restarts the pod instead of sitting
`1/1 Ready` while every reconcile fails with cannot-fork.

## Configuration (env)

| Env | Default | Purpose |
|-----|---------|---------|
| `ROLLOPS_DB` | `rollops.db` | sqlite path (mount a PVC) |
| `ROLLOPS_ADDR` | `:8080` | UI/REST listen address |
| `ROLLOPS_WATCH` | — | path to the watch JSON |
| `ROLLOPS_RECONCILE_INTERVAL` | — | reconcile cadence (e.g. `60s`) |
| `ROLLOPS_ADMIN_TOKEN` | — | bearer token for the REST/gRPC API |
| `ROLLOPS_UI_PASSWORD` | — | UI login password |
| `ROLLOPS_GRPC_ADDR` | — | optional gRPC listen address |
| `ROLLOPS_MCP_ADDR` | — | optional MCP (agent) listen address |
| `ROLLOPS_MCP_TOKENS_FILE` | — | path to a JSON `{token: agent-name}` map for per-caller MCP bearer auth; **preferred** — see [MCP tokens](mcp-tokens.md) |
| `ROLLOPS_MCP_TOKENS` | — | the same JSON inline; ignored when `ROLLOPS_MCP_TOKENS_FILE` is set. MCP is fail-closed: no tokens, no callers |
| `ROLLOPS_ALLOWED_ENV` | — | extra env vars a config-sourced command (smoke test, database hook) may inherit — see [command confinement](command-confinement.md) |
| `ROLLOPS_ALLOWED_COMMANDS` | — | command allowlist for config-sourced commands (opt-in) |
| `ROLLOPS_ALLOWED_NAMESPACES` | — | Kubernetes namespace allowlist (opt-in) |
| `ROLLOPS_CONFINE_TARGET_CLUSTER` | — | ignore repo-supplied kubeconfig/context (opt-in) |
