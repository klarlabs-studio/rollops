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

`deploy/kubernetes/rollopsd.yaml` contains the namespace, ServiceAccount,
ClusterRole/Binding, watch ConfigMap, PVC, Deployment, and Service. RBAC grants
apply/observe on workloads, patch on Gateway API `HTTPRoute`s, and read on CRDs
(for `status.conditions` health). It is cluster-scoped for simplicity; narrow it
to per-namespace Roles in stricter setups.

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

## Apply

```sh
kubectl apply -f deploy/kubernetes/rollopsd.yaml
kubectl -n rollops-system rollout status deploy/rollopsd
kubectl -n rollops-system port-forward svc/rollopsd 8080:80   # open http://localhost:8080
```

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
