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

- **Multi-org** (klarlabs-studio + felixgeelhaar, no SSO): one App is *installed*
  on each org → two installations; each watched repo names its org's
  `githubInstallationId`. Deploy keys are **per-repo** — N keys to create,
  distribute, and rotate; the App is one key covering every repo in both orgs.
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

## Prerequisites (one-time)

1. **Create the GitHub App** (owner: `klarlabs-studio` org). Settings:
   - Permissions: **Repository → Contents: Read and write** (write is required —
     image automation commits digest/semver bumps back and pushes;
     `internal/reconcile/imageauto.go`). **Metadata: Read** (implicit).
   - No webhook (the daemon **polls**; it does not receive webhooks for reconcile).
   - Where can it be installed: *Only on this account* is fine if both orgs are
     under the same ownership; otherwise make it installable on both.
   - Generate a **private key** (PEM) — download once.
2. **Install the App** on **both** orgs, scoped to *Only select repositories* =
   the repos rollopsd watches (28-ish first-party deployments). Record the
   **Installation ID** for each org (`…/installations/<id>` in the install URL,
   or via the API).
3. **Store the private key as a k8s Secret** in `rollops-system`, mounted into
   the `rollopsd` pod at a stable path (e.g. `/etc/rollops/github-app.pem`).
   ONE secret replaces the `rollopsd-git` PAT secret.

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
  "githubAppId": "<app-id>",
  "githubInstallationId": "<klarlabs-studio-installation-id>",
  "githubAppPrivateKeyFile": "/etc/rollops/github-app.pem" }
```

- `felixgeelhaar`-org repos use that org's installation ID; same App, same key.
- The three App keys are **required together** and **take precedence** over
  `token`/`tokenFile`, so a repo entry can carry both during transition — App
  wins — then the token keys are deleted.

**Cutover order (low-risk):**
1. Pick one low-criticality repo. Add the App keys (keep the token). Deploy.
2. Verify in logs: clone/pull succeeds and the per-tick image-automation line
   shows the repo `current`/`bumped` (not an auth error). Confirm a write-back
   still pushes (trigger or wait for a bump).
3. Roll the rest in batches; the App is a config-only change per repo (no image
   rebuild needed if the watch config is a mounted file/ConfigMap; a restart
   picks it up).
4. Once **all** repos run on the App: remove `token`/`tokenFile` from every
   entry, delete the `rollops-git` PAT secret, and **revoke the classic PAT** on
   GitHub. (The leaked PAT was already revoked 2026-07-17; this decommissions its
   replacement.)

## Secret rotation (App private key)

1. In the App settings, **generate a new private key** (both old and new are
   valid until you delete the old).
2. Update the k8s Secret with the new PEM; restart `rollopsd` (it reads the key
   at startup / per-App-construction).
3. Delete the old key in the App settings.
   No token distribution, no per-repo churn — one file, one restart.

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

## Open questions for the operator

1. One App installed on both orgs, or a separate App per org? (One App is
   simpler; two isolate blast radius.)
2. Which repos get `contents: write` (image write-back) vs read-only? The current
   PAT is uniform; the App can split read-only repos from writable ones by using
   *two* installations with different permission sets if you want tighter scope.
3. Is the watch config a mounted file/ConfigMap (restart-to-reload) or baked into
   the image? Determines whether cutover is a config edit + restart or a redeploy.
