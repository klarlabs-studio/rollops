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
| `/metrics`, `/readyz` | Server TLS 1.3 | None — unauthenticated scrape/probe endpoints |

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
rollopsd: MCP serving on :8444 as agent "local" (TLS=on mTLS=on)
```

## Certificate rotation (hot-reload)

The server certificate is served through a `GetCertificate` callback that
re-reads the keypair from disk whenever the certificate file's modification time
changes. An operator (or cert-manager) can rotate the mounted Secret in place and
the running daemon serves the new leaf on the next handshake — **no restart**.
The last successfully parsed keypair is cached and kept in service if a rotation
writes a transiently unreadable/partial file, so rotation can never take a
listener down.

## cert-manager setup (Kubernetes)

`deploy/kubernetes/rollopsd.yaml` ships a working default: a self-signed
`ClusterIssuer` and a `Certificate` that issues the server cert (with the
in-cluster DNS SANs `rollopsd.rollops-system.svc` and
`rollopsd.rollops-system.svc.cluster.local`) into the `rollopsd-tls` Secret. The
Secret is mounted at `/etc/rollops/tls` and wired via `ROLLOPS_TLS_CERT`,
`ROLLOPS_TLS_KEY`, and `ROLLOPS_TLS_CLIENT_CA` (`ca.crt` from the same Secret).

**Prerequisite:** cert-manager must be installed
(<https://cert-manager.io/docs/installation/>). Without it the Issuer/Certificate
are inert and no `rollopsd-tls` Secret is created, so the pod won't start (the
TLS mount stays unsatisfied). Either install cert-manager, supply your own
`rollopsd-tls` Secret (keys `tls.crt`, `tls.key`, `ca.crt`), or use the mesh
alternative below.

**Swap for your real CA.** The self-signed issuer is only a stand-alone default.
In production point the `Certificate`'s `issuerRef` at a real CA — Let's Encrypt
(ACME), Vault, or an intermediate-CA Issuer — so client and server certs chain
to a trust root you control.

## Issuing client certificates for CLIs and agents

With mTLS enabled, any client of the REST API / gRPC / MCP surfaces must present
a certificate signed by the CA in `ROLLOPS_TLS_CLIENT_CA`. Two common paths:

**cert-manager `Certificate` (in-cluster clients):** issue a client cert from the
same issuer, requesting the client-auth usage, into a Secret the client mounts.

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
  issuerRef: # the SAME issuer/CA rollopsd trusts
    name: rollopsd-selfsigned
    kind: ClusterIssuer
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
