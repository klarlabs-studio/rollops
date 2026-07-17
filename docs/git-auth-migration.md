# Git auth migration: classic PAT → GitHub App (roady #34)

**Status:** design / runbook — no code changes required.
**Author:** RollOps maintenance, 2026-07-17.

## TL;DR

The daemon already supports **three** git-auth modes per watched repo
(`buildWatchSpecs` in `cmd/rollopsd/main.go`, `git.Auth`):

| Mode | Config keys | Secret to manage | Rotation |
|---|---|---|---|
| Classic PAT (today) | `token` / `tokenFile` | one long-lived token, org-wide reach | manual |
| SSH deploy key | `deployKeyPath` | **one key per repo** | manual, per key |
| **GitHub App** (recommended) | `githubAppId` + `githubInstallationId` + `githubAppPrivateKeyFile` | **one App private key** | auto (installation tokens expire ≤1h; `internal/git/githubapp.go` re-mints within 60s of expiry) |

roady #34 ("least-privilege multi-org git auth to replace the broad classic
PAT") is therefore **not a build** — `internal/git/githubapp.go` implements App
token minting and `git.Auth.TokenSource` resolves a fresh token per git command.
What remains is the **operational migration** below.

## Recommendation: GitHub App (not deploy keys)

- **Multi-org** (klarlabs-studio + felixgeelhaar, no SSO): a **separate App per
  org** (decided 2026-07-17 — best practice: a compromised or revoked key is
  blast-radius-contained to one org). Two Apps, each owned by and installed on
  its own org, each with its own private key and installation ID; every watched
  repo names its org's App ID + key. Deploy keys are **per-repo** — N keys to
  create, distribute, and rotate; two Apps cover every repo in their org.
- **Least-privilege:** App permissions are fine-grained (`contents: read`, plus
  `contents: write` only where image write-back runs) and can be installed on
  *selected repositories* per org — far tighter than a classic PAT, which reaches
  every repo the owning account can.
- **Auto-rotation:** installation tokens are short-lived (~1h) and minted on
  demand; the only long-lived secret is the App private key (one file), vs a
  standing PAT.
- **Higher rate limits** than a user PAT, and commits/pushes from image
  automation are attributed to the App (a bot identity), not a person.

Deploy keys remain a valid fallback for a one-off external repo the App can't be
installed on; the config supports mixing modes per repo.

## Prerequisites (one-time) — two Apps, one per org

Do this **twice**, once per org (`klarlabs-studio`, `felixgeelhaar`):

1. **Create a GitHub App** owned by the org. Settings (identical for both):
   - Permissions: **Repository → Contents: Read and write** (write is required —
     image automation commits digest/semver bumps back and pushes;
     `internal/reconcile/imageauto.go`). **Metadata: Read** (implicit).
   - No webhook (the daemon **polls**; it does not receive webhooks for reconcile).
   - Where can it be installed: *Only on this account*.
   - Generate a **private key** (PEM) — download once. Note the **App ID**.
2. **Install the App** on its org, scoped to *Only select repositories* = the
   repos rollopsd watches in that org. Record the **Installation ID**
   (`…/installations/<id>` in the install URL, or via the API).
3. **Store each private key as its own k8s Secret** in `rollops-system`, mounted
   into the `rollopsd` pod at a stable per-org path, e.g.
   `/etc/rollops/github-app-klarlabs.pem` and `/etc/rollops/github-app-fg.pem`.
   Two secrets replace the single `rollopsd-git` PAT secret — separate keys are
   the whole point of separate Apps; don't share one key between the two.

You end up with two triples: `(klarlabsAppId, klarlabsInstallationId,
github-app-klarlabs.pem)` and the `felixgeelhaar` equivalent.

## Migration steps

The watch config is JSON (per `buildWatchSpecs`). For each repo, replace the PAT
keys with the App keys, picking the installation for that repo's org:

```jsonc
// before
{ "name": "brotwerk-api", "url": "https://github.com/klarlabs-studio/brotwerk",
  "branch": "main", "path": ".rollops", "tokenFile": "/etc/rollops/git-pat" }

// after
{ "name": "brotwerk-api", "url": "https://github.com/klarlabs-studio/brotwerk",
  "branch": "main", "path": ".rollops",
  "githubAppId": "<klarlabs-studio-app-id>",
  "githubInstallationId": "<klarlabs-studio-installation-id>",
  "githubAppPrivateKeyFile": "/etc/rollops/github-app-klarlabs.pem" }
```

- `felixgeelhaar`-org repos use the **felixgeelhaar App** — its own App ID,
  installation ID, and `github-app-fg.pem` (separate App per org).
- The three App keys are **required together** and **take precedence** over
  `token`/`tokenFile`, so a repo entry can carry both during transition — App
  wins — then the token keys are deleted.

**Cutover order (low-risk):**
1. Pick one low-criticality repo. Add the App keys (keep the token). Deploy.
2. Verify in logs: clone/pull succeeds and the per-tick image-automation line
   shows the repo `current`/`bumped` (not an auth error). Confirm a write-back
   still pushes (trigger or wait for a bump).
3. Roll the rest in batches. Cutover is **config-only**: the watch list is the
   `rollopsd-watch` ConfigMap mounted at `ROLLOPS_WATCH=/etc/rollops/watch.json`
   (see `deploy/kubernetes/rollopsd.yaml`), so a migration is a **ConfigMap edit
   + pod restart** — no image rebuild.
4. Once **all** repos run on the App: remove `token`/`tokenFile` from every
   entry, delete the `rollopsd-git` PAT secret, and **revoke the classic PAT** on
   GitHub. (The leaked PAT was already revoked 2026-07-17; this decommissions its
   replacement.)

## Secret rotation (App private key)

Each org's key rotates independently — that isolation is the point of two Apps:

1. In that App's settings, **generate a new private key** (both old and new are
   valid until you delete the old).
2. Update that org's k8s Secret with the new PEM; restart `rollopsd` (it reads
   the keys at startup / per-App construction).
3. Delete the old key in the App settings.
   No token distribution, no per-repo churn — one file, one restart, one org.

## Risks / caveats

- **Write scope:** `contents: write` is org/installation-wide across the
  *selected repositories*. Keep the install list tight; don't grant the App
  more repos than rollopsd watches.
- **Token expiry mid-operation:** installation tokens last ~1h; the code re-mints
  within 60s of expiry (`githubapp.go:Token`), so long syncs are safe.
- **Commit attribution:** image-automation commits/pushes appear as the App
  (bot). Confirm that's acceptable for the app repos' history/branch protection
  (an App can be allowed to bypass required reviews if needed).
- **Rollback:** because App keys take precedence but coexist with `token`, a
  bad cutover is reverted by removing the App keys from the entry (token remains)
  and restarting — no data loss.

## Decisions & remaining choice

- **Separate App per org** — decided 2026-07-17 (best practice: a compromised or
  revoked key is blast-radius-contained to one org). Two Apps, two keys, two
  installations.
- **Cutover = ConfigMap edit + pod restart** — confirmed: the watch list is the
  `rollopsd-watch` ConfigMap (`ROLLOPS_WATCH=/etc/rollops/watch.json`,
  `deploy/kubernetes/rollopsd.yaml`), not baked into the image.
- **Permission split (open, low-stakes):** the current PAT is uniform
  `contents: write`. Because image write-back runs fleet-wide, the recommendation
  is to keep each org's App at `contents: write` on its selected repos. Only
  split into a read-only App + a write App if some watched repos must never be
  written to — otherwise it's not worth the extra installation. Defaulting to
  uniform unless the operator flags watch-only repos.
