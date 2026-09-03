# TLS & mTLS (zero-trust transport)

rollopsd terminates TLS itself on every network listener. The posture is
zero-trust by default:

- **TLS 1.3 on every non-loopback bind.** There is no plaintext escape hatch.
- **mTLS (require + verify a client certificate) on the machine control plane:**
  the programmatic REST API, gRPC, and the MCP agent surface.
- **The human web console (UI) keeps server-TLS + OIDC/session auth** and does
  *not* require a client certificate — browsers can't present one, so strong SSO
  is the control there.
- **Loopback binds may stay plaintext** for a same-host reverse proxy or an
  in-pod sidecar/mesh hop that encrypts at the network boundary.

## Environment variables

| Variable | Meaning |
| --- | --- |
| `ROLLOPS_TLS_CERT` | Path to the server certificate PEM (leaf + any intermediates). |
| `ROLLOPS_TLS_KEY` | Path to the server private key PEM. Must be set together with `ROLLOPS_TLS_CERT`. |
| `ROLLOPS_TLS_CLIENT_CA` | Optional. Path to a PEM CA bundle used to verify client certificates. Setting it enables **mTLS** on the REST API, gRPC, and MCP surfaces. |

- If neither `ROLLOPS_TLS_CERT` nor `ROLLOPS_TLS_KEY` is set, the daemon runs in
  **plaintext mode**, which is only valid on a loopback bind. A non-loopback bind
  without TLS is refused at startup:

  ```
  refusing to serve HTTP on non-loopback address ":8080" without TLS:
  set ROLLOPS_TLS_CERT and ROLLOPS_TLS_KEY (see docs/tls.md)
  ```

- Setting one of `ROLLOPS_TLS_CERT` / `ROLLOPS_TLS_KEY` without the other is a
  startup error, as is setting `ROLLOPS_TLS_CLIENT_CA` without a server keypair
  (mTLS needs a server cert to terminate TLS first).

## Per-surface behavior

| Surface | Transport | Client auth when `ROLLOPS_TLS_CLIENT_CA` is set |
| --- | --- | --- |
| Web console (`/ui`, `/ui/`) | Server TLS 1.3 | **None** — server TLS + OIDC/session auth only |
| REST API (`/`) | Server TLS 1.3 | **Required** — a verified client cert, else `401` |
| gRPC (`ROLLOPS_GRPC_ADDR`) | Server TLS 1.3 | **Required** — `RequireAndVerifyClientCert` |
| MCP (`ROLLOPS_MCP_ADDR`) | Server TLS 1.3 | **Required** — `RequireAndVerifyClientCert` |
| `/metrics`, `/readyz`, `/livez` | Server TLS 1.3 | None — unauthenticated scrape/probe endpoints |

The REST API and the web console share one HTTPS listener. That listener runs
`ClientAuth = VerifyClientCertIfGiven`, so the TLS stack verifies a client cert
against the CA *if one is presented* (letting the browser UI connect without
one). The REST API handler then rejects any request that did not present a
verified client cert, giving per-surface mTLS on a single port. gRPC and MCP
run on their own listeners with `RequireAndVerifyClientCert`.

The active posture is logged at startup, e.g.:

```
rollopsd: listening on :8443 (db ...) HTTP TLS=on mTLS(api)=on
rollopsd: gRPC on :8443 (TLS=on mTLS=on)
rollopsd: MCP serving on :8444 (per-caller bearer auth, 2 token(s), TLS=on mTLS=on)
```

The MCP surface authenticates each caller by a bearer token on top of mTLS: mTLS
proves a trusted client, and the token proves *which* caller (resolving to a
distinct `rollout.Identity` so RBAC applies per caller).

Configure the tokens with **`ROLLOPS_MCP_TOKENS_FILE`**, a path to a JSON object
mapping each token to an agent name:

```json
{"<token-a>": "nomi", "<token-b>": "deploy-bot"}
```

```
ROLLOPS_MCP_TOKENS_FILE=/etc/rollops/mcp-tokens.json
```

The same JSON can be supplied inline as `ROLLOPS_MCP_TOKENS`, but a file is
preferred — see [MCP tokens](mcp-tokens.md) for why, and for the rotation
procedure. When both are set the file wins and the env var is ignored (logged at
startup).

Callers pass `Authorization: Bearer <token>`. The surface is **fail-closed**: with
no token map configured, or a token that does not resolve, the request is rejected
(`403`) before any tool runs — there is no fallback identity.

## Certificate rotation (hot-reload)

The server certificate is served through a `GetCertificate` callback that
re-reads the keypair from disk whenever the certificate file's modification time
changes. An operator (or cert-manager) can rotate the mounted Secret in place and
the running daemon serves the new leaf on the next handshake — **no restart**.
The last successfully parsed keypair is cached and kept in service if a rotation
writes a transiently unreadable/partial file, so rotation can never take a
listener down.

## cert-manager setup (Kubernetes)

`deploy/kubernetes/rollopsd.yaml` ships a working default **CA chain** (mTLS
needs both the server cert and every client cert to chain to one shared CA — a
bare self-signed leaf can't verify a separately-issued client cert):

1. a self-signed `ClusterIssuer` (`rollopsd-selfsigned`) — bootstrap only;
2. a root CA `Certificate` (`rollopsd-ca`, `isCA: true`) signed by it, into the
   `rollopsd-ca` Secret;
3. a CA `Issuer` (`rollopsd-ca`) backed by that Secret; and
4. the server `Certificate` (`rollopsd-tls`, `usages: [server auth]`, with the
   in-cluster DNS SANs `rollopsd.rollops-system.svc` and
   `rollopsd.rollops-system.svc.cluster.local`) signed by the CA Issuer.

The server Secret is mounted at `/etc/rollops/tls` and wired via `ROLLOPS_TLS_CERT`,
`ROLLOPS_TLS_KEY`, and `ROLLOPS_TLS_CLIENT_CA` (`ca.crt` from the same Secret — the
root CA, which therefore verifies client certs issued from the same CA Issuer).

**Prerequisite:** cert-manager must be installed
(<https://cert-manager.io/docs/installation/>). Without it the Issuers/Certificates
are inert and no `rollopsd-tls` Secret is created, so the pod won't start (the
TLS mount stays unsatisfied). Either install cert-manager, supply your own
`rollopsd-tls` Secret (keys `tls.crt`, `tls.key`, `ca.crt`), or use the mesh
alternative below.

**Swap for your real CA.** The self-signed root is only a stand-alone default. In
production point the `rollopsd-ca` `Issuer` at your real CA — Vault, an
intermediate-CA Issuer, or step-ca — so server and client certs chain to a trust
root you control.

## CLI client (`ROLLOPS_DAEMON`)

The one-shot CLI talks to a running daemon over gRPC when `ROLLOPS_DAEMON` is
set. It uses the **same** `ROLLOPS_TLS_*` variables as rollopsd:

- Unset → plaintext (the loopback dev default).
- `ROLLOPS_TLS_CERT` (+ `ROLLOPS_TLS_KEY`) → TLS 1.3; the cert PEM is trusted
  as the server root (self-signed dogfood keypair, or a concatenated chain).
- `ROLLOPS_TLS_CLIENT_CA` set → the same keypair is presented as the client
  certificate so mTLS CLI calls work without a second identity.

```sh
export ROLLOPS_DAEMON=rollopsd.example:8090
export ROLLOPS_TOKEN=...
export ROLLOPS_TLS_CERT=/etc/rollops/tls/tls.crt
export ROLLOPS_TLS_KEY=/etc/rollops/tls/tls.key
# mTLS:
export ROLLOPS_TLS_CLIENT_CA=/etc/rollops/tls/ca.crt
bin/rollops status <rollout-id>
```

## Issuing client certificates for CLIs and agents

With mTLS enabled, any client of the REST API / gRPC / MCP surfaces must present
a certificate signed by the CA in `ROLLOPS_TLS_CLIENT_CA`. Two common paths:

**cert-manager `Certificate` (in-cluster clients):** issue a client cert from the
**same CA `Issuer`** (`rollopsd-ca`) rollopsd's server cert uses, requesting the
client-auth usage, into a Secret the client mounts. A ready-to-copy example ships
at `deploy/kubernetes/rollops-client-cert.example.yaml`:

```yaml
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: rollops-agent
  namespace: rollops-system
spec:
  secretName: rollops-agent-tls
  duration: 720h
  renewBefore: 120h
  privateKey: { algorithm: ECDSA, size: 256, rotationPolicy: Always }
  commonName: rollops-agent
  usages: [client auth]
  issuerRef: # the SAME CA Issuer rollopsd's server cert chains to
    name: rollopsd-ca
    kind: Issuer
    group: cert-manager.io
```

The client then presents `tls.crt` / `tls.key` and trusts the server via the CA
(`ca.crt`). Example with `grpcurl`:

```sh
grpcurl -cacert ca.crt -cert tls.crt -key tls.key \
  -H 'authorization: Bearer <token>' rollopsd.rollops-system.svc:443 ...
```

or `curl` against the REST API:

```sh
curl --cacert ca.crt --cert tls.crt --key tls.key \
  -H 'Authorization: Bearer <token>' https://rollopsd.rollops-system.svc/rollouts
```

Note that mTLS is a transport control layered **on top of** the existing
bearer-token / OIDC authentication and RBAC — a valid client cert gets you onto
the wire; the token still identifies and authorizes the principal.

**Operator-distributed client cert (VPS / systemd):** issue a client cert from
your CA out of band (your PKI, `step-ca`, Vault, or `openssl`) and distribute the
keypair + CA bundle to each CLI/agent host.

## Loopback + mesh alternative (no daemon certs)

If you run a service mesh (Istio, Linkerd) or a same-pod TLS sidecar, you can let
it own mTLS at the network boundary instead of issuing certs to the daemon:

- Bind loopback only: `ROLLOPS_ADDR=127.0.0.1:8080` (and loopback gRPC/MCP addrs).
- Leave `ROLLOPS_TLS_*` unset — a loopback bind needs no TLS.
- Let the mesh sidecar terminate/originate mTLS for traffic entering/leaving the
  pod.

In this model the daemon speaks plaintext only over loopback (never the network),
and the mesh provides the zero-trust transport guarantees.
