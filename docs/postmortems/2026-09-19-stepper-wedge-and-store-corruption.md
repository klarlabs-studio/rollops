# Post mortem: rollouts wedged for a month, then the store went corrupt

**Date of writeup:** 2026-09-19
**Incident window:** 2026-08-19 → 2026-09-19 (31 days)
**Severity:** high — six production targets could not be deployed to; no alert ever fired
**Status:** code fixed (`20d1f3e`), not yet released or deployed; several action items open

---

## Summary

Four production targets sat in a permanently unrecoverable rollout state for up to a
month. Every reconcile — once a minute, 282 times per target — logged an error, changed
nothing, and left the target claimed. Any new rollout for those targets failed with
`ErrTargetBusy`. Two further targets were stuck in a second, quieter way that logged
`target busy` and nothing else.

Nobody noticed, because nothing was watching. The condition was visible only in container
logs that no one reads, and the daemon reported itself healthy throughout.

While this was being investigated the SQLite store became corrupt
(`database disk image is malformed`, SQLITE_CORRUPT), which took the daemon from
"wedged on six targets" to "cannot read any rollout at all". The store was quarantined and
rebuilt empty. That ended the wedge by destroying the records that constituted it, not by
repairing anything.

Both the wedge bug and the detection gap are real and independent. The fix addresses the
first. The second is still open.

---

## Impact

| Target | Rollout | Wedged since | Symptom |
|---|---|---|---|
| `glossa/admin` | `ro-20260819T044203.324876208` | 2026-08-19 | deploying, no snapshot |
| `kraftsport-coach/app` (Ingress) | `ro-20260819T114249.431383553` | 2026-08-19 | deploying, no snapshot |
| `kraftsport-coach/minio` (Service) | `ro-20260825T081530.483636137` | 2026-08-25 | deploying, no snapshot |
| `brotwerk/marketing-site` (Deployment) | `ro-20260916T033202.876476015` | 2026-09-16 | deploying, no snapshot |
| `vorhut/prod/dep-redis` | — | unknown | `target busy` (paused rollout) |
| `vorhut/prod/vorhut-executor` | — | unknown | `target busy` (paused rollout) |

No outage resulted. The live workloads kept running; what was lost was the ability to
deploy *changes* to them. That is the reason it went unnoticed for a month — the failure
mode is silent stasis, not breakage.

All rollout history for every target was subsequently lost with the store.

---

## Root cause: a durable state no code could leave

`Apply` writes the rollout's phase and the stepper writes the first snapshot, and there is
a slow Kubernetes operation between them:

```
engine.go:934    SaveRollout(Phase = PhaseDeploying)     ← durable
engine.go:947    deployOnce(...)                          ← kubectl apply + rollout wait
engine.go:950    startStepper → driveStepper
stepper.go:172     recordStep() → SaveRollout             ← still an empty snapshot
stepper.go:242     snapshot written
stepper.go:258     SaveRollout(StepperSnap = ...)         ← durable
```

A process death anywhere between line 934 and line 258 leaves a row that is
`Deploying` with an empty `StepperSnap`. The window is not narrow: `deployOnce` waits on a
real Kubernetes rollout, so it is seconds to minutes wide, and `recordStep()` saves again
in the middle of it without closing the gap.

`Tick` then refused that row outright:

```go
if len(r.StepperSnap) == 0 {
    return &r, fmt.Errorf("engine: tick: rollout %s is deploying without a stepper snapshot", r.ID)
}
```

The error was correct and useless. Nothing in the engine could produce that state
in-process, so it was treated as an assertion failure rather than a recoverable condition.
But the state is reachable — it just requires the process to die, which it does often (see
below) — and once reached, nothing could clear it.

### Why the target stayed claimed

This is what turned a bad row into a month-long outage. Occupancy is not a separate lock
with its own lifetime; **occupancy between ticks *is* the deploying phase**. `InFlight`
derives busy-ness from the rollout's phase. So a rollout that can never leave `Deploying`
is a rollout that can never release its target, and every subsequent `Apply` for that
target fails with `ErrTargetBusy` forever.

A failure that merely logged would have been noise. A failure that logs *and* holds a
mutex it can never release is an outage.

### The second, quieter variant

`vorhut/prod/dep-redis` and `vorhut/prod/vorhut-executor` show `target busy` rather than
the snapshot error. `Tick` returns `nil` for `PhasePaused` — a paused rollout occupies its
target and logs nothing at all. Same consequence, no diagnostic whatsoever. These two need
`resume`, not recovery, and were never the same bug.

---

## Why it lasted 31 days

The bug made the state unreachable-by-design. These are the reasons nobody found out.

### 1. `/readyz` is a lie, and it is also the liveness probe

```go
top.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
```

It checks nothing. It returns 200 as long as the HTTP server is listening. The deployment
probes it for **both** readiness and liveness:

```
readiness=/readyz   liveness=/readyz
```

So the only health signal Kubernetes had was "the process is accepting connections". A
daemon failing every reconcile satisfies that perfectly. So does a daemon whose database is
corrupt — which is exactly what happened this morning, with the pod reporting `1/1 Running`
throughout.

### 2. The lesson was learned, coded, committed — and never rolled out

`/livez` exists in `main` and does real work: it fails when the cgroup is out of PID slots.
Its comment names precisely this class of failure:

```go
// /livez fails when the cgroup is nearly out of process slots — the gap
// the v0.34.3 zombie leak exposed: readiness stayed green while every
// reconcile failed with "cannot fork". See internal/procgroup.
```

The repository's own deployment manifest, `deploy/kubernetes/rollopsd.yaml`, probes it
correctly, and carries the same reasoning:

```yaml
# /readyz is "process is up"; /livez is "still able to fork". The
# v0.34.3 zombie leak left readiness green while every reconcile
# failed with cannot-fork — liveness must catch that.
livenessProbe:
  httpGet: { path: /livez, port: https, scheme: HTTPS }
```

So the remediation was written, committed, and declared in the manifest. The live cluster
has none of it:

```
live:   readiness=/readyz   liveness=/readyz
image:  rollopsd:v0.34.3
```

`/livez` shipped in **v0.34.5** (`38c9f7a`, first tag containing it). `git show
v0.34.3:cmd/rollopsd/main.go | grep -c /livez` → **0**. The endpoint does not exist in the
running binary, so the repo's manifest could not have been applied as-is without
CrashLooping the pod on a 404. The live Deployment is pinned to the older probe config, and
carries `kubectl.kubernetes.io/last-applied-configuration` — applied by hand, out of band.

The actual gap is therefore not a forgotten probe. **The cluster is two releases behind,
and the fix for the previous incident is in a release that was never rolled out.** The
resource-limit change from 2026-08-10 *did* reach the cluster (live matches the repo at
250m/128Mi, 1500m/384Mi), which shows the manifest is applied selectively and by hand
rather than as a unit.

Worth stating plainly: RollOps is a GitOps reconciler that is itself deployed out of band,
has drifted from its own declared manifest, and runs in detect-only mode — so it would not
self-correct even if it watched itself.

This is the single most actionable finding in the document.

### 3. Metrics exist; nothing scrapes them

`internal/metrics/metrics.go` exports exactly what was needed:

- `reconcileTotal` (CounterVec, labelled by result)
- `rolloutOutcomes`
- `driftTotal`
- `reconcileLatency`

served on `/metrics`. And:

- the Deployment declares **one** port, `8443/https` — no metrics port
- there is **no ServiceMonitor** for rollops anywhere in the cluster
- **zero of 38 PrometheusRules** mention rollops

`rate(reconcileTotal{result="error"}[5m]) > 0` would have fired on 2026-08-19, within
minutes of the first wedge, and then continuously for a month. The instrumentation was
complete and entirely unobserved.

### 4. Drift-detect mode makes stasis look normal

Reconcile logs drift continuously without correcting it (`detect` mode — 242 drift lines in
a single current window). Divergence between Git and live is the *steady state* of this
system. Against that background a target that never converges is indistinguishable from
every other target that never converges.

### 5. The evidence expired

Kubernetes events retain roughly 47 minutes here and `revisionHistoryLimit` is 10. The
restarts that caused the 2026-08-19, 08-25 and 09-16 wedges have aged out. We can describe
the mechanism precisely from the source, but we cannot recover what killed the process on
any of the four occasions. Post-hoc forensics on this cluster has a sub-hour horizon.

---

## Contributing factor: the process dies a lot

The bug needs a process death in a specific window. The deployment maximises the supply:

- `strategy: Recreate` with `replicas: 1` — every rollout kills the running process
  outright, with no second replica to carry in-flight state
- **79 deployment generations** to date; 73 since 2026-06-13
- `terminationGracePeriodSeconds: 30`
- `memory limit: 384Mi` — an OOMKill is SIGKILL, no graceful shutdown, straight into the
  window

Each of those 79 restarts was a chance to land in the gap. Four of them did, that we know
of.

---

## Second act: the store went corrupt

Mid-investigation, on 2026-09-19 at `09:29:36Z`, rollopsd began logging
`sqlite: list rollouts: database disk image is malformed (11)` — 847 occurrences in eleven
minutes, and still climbing when it was stopped.

This masked the original symptom completely: all six stuck-target errors dropped to zero,
because `ListRollouts` is what `InFlight`, `priorManifest` and `Tick` all depend on, and it
was now failing before it could reach any of them. The alert count going to zero meant the
system had stopped being able to read anything, not that it had recovered. Worth
remembering the next time a metric improves suddenly.

### Response

A Job renamed the malformed files rather than deleting them:

```
before:  rollops.db  (4,878,336 bytes)
after:   corrupt-20260919T094151Z-rollops.db
```

rollopsd restarted onto the empty volume, built a fresh database, and came back clean:
`1/1 Running`, 10 repos watched, zero malformed errors, zero stuck-target errors, zero
errors of any kind.

The corrupt image is preserved on the `rollopsd-data` volume. `sqlite3 .recover` has not
been attempted.

**This is not a repair.** Every rollout record was destroyed, including the four wedged
rows. The wedge is gone because its evidence is gone. One incidental consequence:
`priorManifest` now finds no prior checksum for any target, so `Abort` would no longer roll
anything back — the six-week image regression that `abort brotwerk/marketing-site` would
have caused is moot, by accident rather than by decision.

### Suspected cause of the corruption — unproven

The SQLite configuration is not obviously at fault:

```go
dsn := "file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(on)"
```

WAL is on, `synchronous` is unset and therefore `FULL`. That is a conservative, safe
configuration.

Attention falls on the storage layer instead. `rollopsd-data` is 1Gi on **`longhorn-r1`**:

```
numberOfReplicas: "1"
disableRevisionCounter: "true"
staleReplicaTimeout: "30"
reclaimPolicy: Delete
```

A single replica, no revision counter, `Delete` reclaim, and a volume that detaches and
reattaches on every one of those 79 restarts — including across nodes. The live volume
currently reports `replicas=2, robustness=healthy`, so it has been adjusted at some point.

Separately, **two StorageClasses are both marked default**:

```
local-path (default)
longhorn-r1 (default)
```

That is a misconfiguration in its own right and makes the placement of any new PVC
non-deterministic.

Root cause of the corruption is **not established**. It is plausible that an unclean kill
during a write, on a single-replica volume with the revision counter disabled, is
sufficient — but that is a hypothesis, not a finding.

### Unresolved: who ran `rollops-dbfix`

A busybox pod named `rollops-dbfix`, mounting the rollopsd PVC, ran on edge-2 at
approximately `09:27Z` — roughly two minutes *before* the first malformed error. It ran
again later and held the RWO volume long enough to block rollopsd's reattach with a
`Multi-Attach error`.

Nothing in this investigation created it deliberately, and the operator is unidentified. A
pod mounting the database volume immediately before the database became malformed is a
correlation that must be run down before the storage-layer hypothesis above is accepted.
**Whoever ran it should say what it did.**

---

## What was fixed

Commit `20d1f3e`, `fix(engine): recover a deploy orphaned before its first snapshot`.

`Tick` now recovers an orphaned deploy instead of refusing it in a loop. Three design
points, each of which was a decision rather than an obvious step:

**The check moved below `acquireTarget`.** It previously ran first, on the reasonable
theory that you should reject a bad row cheaply. But holding the target lease is precisely
what proves no `Apply` is mid-flight: `AcquireLease` is TTL-based and a crashed process's
lease expires on its own. That is a *proof*, not a staleness heuristic with a tunable
threshold, and it is the entire reason recovery is safe rather than a race against a live
peer.

**Recovery re-applies the desired manifest** rather than assuming the cluster already
converged. `deployOnce` is the likeliest thing the crash interrupted; `Apply` is idempotent;
and presuming convergence is how a recovery deploys nothing while reporting success.

**Recovery moves forward only.** No rollback. Git is the operator's stated intent, and a
recovery path that reverts toward an older manifest would be fighting it.

An audit entry (`recovered orphaned deploy (no stepper snapshot); re-applying desired
manifest`) is emitted, because silently self-healing a state that is supposed to be
unreachable is how the next such window goes unnoticed for another month.

Three tests, written red first:

- `TestTick_OrphanedDeployRecoversInsteadOfWedging` — the regression test proper
- `TestTick_OrphanedDeployDoesNotStealALiveApply` — guards the premise; with the lease held
  elsewhere, `Tick` must return `ErrTargetBusy` and re-apply nothing
- `TestTick_OrphanRecoveryIsAudited` — the recovery stays visible

Full suite green (39 packages).

---

## Action items

Ordered by the gap they close, not by effort.

| # | Action | Why | Owner |
|---|---|---|---|
| 1 | **Upgrade rollopsd from v0.34.3 and apply `deploy/kubernetes/rollopsd.yaml` as a unit** | Brings `/livez` (v0.34.5) and the manifest's correct liveness probe together; neither works without the other | open |
| 2 | **Stop applying the rollopsd manifest by hand.** Reconcile it from Git like everything else, or record explicitly why it is exempt | The live Deployment has drifted from the repo's own manifest; selective hand-application is what stranded the probe fix | open |
| 3 | **Give `/readyz` real checks** — at minimum a store read — or stop probing it | It currently cannot fail, including when the database is unreadable | open |
| 4 | **Expose and scrape `/metrics`**: add the container port, a Service port, and a ServiceMonitor | Instrumentation is complete and entirely unobserved | open |
| 5 | **Alert on `rate(reconcileTotal{result="error"}[5m]) > 0`** and on a rollout held in a non-terminal phase beyond a threshold | Zero of 38 PrometheusRules mention rollops; this alert would have fired on day one | open |
| 6 | **Release `20d1f3e`** and include it in the upgrade above | The deployed v0.34.3 will recreate the wedge on the next badly-timed restart | open |
| 7 | **Resolve the two paused vorhut targets** (`dep-redis`, `vorhut-executor`) via `resume` | Different bug, still outstanding, and it logs nothing | open |
| 8 | **Identify `rollops-dbfix`** and what it did to the volume | Ran two minutes before the corruption began; blocks the storage hypothesis | open |
| 9 | **Move `rollopsd-data` off `longhorn-r1`**; raise replicas, set `reclaimPolicy: Retain` | Single replica, revision counter disabled, `Delete` reclaim, for the daemon's only durable state | open |
| 10 | **Remove one of the two default StorageClasses** | `local-path` and `longhorn-r1` are both marked default | open |
| 11 | **Attempt `sqlite3 .recover`** on `corrupt-20260919T094151Z-rollops.db` | It is the only remaining copy of all rollout history | open |
| 12 | **Raise event retention / `revisionHistoryLimit`** | Forensics currently has a ~47-minute horizon; three of four wedge causes are unknowable | open |
| 13 | **Audit for other unreachable-state assertions** that return an error from a reconcile path while holding occupancy | The bug class, not the bug | open |

---

## What this incident is actually about

The engine bug is ordinary — a durable write and a slow operation in the wrong order, the
kind of thing that gets fixed in forty lines. It became a month-long outage because of the
layer above it:

- an error that could only be reported, never acted on
- a lock whose lifetime was a phase rather than a lease
- a health endpoint that returns 200 unconditionally, used as the liveness probe
- a real health endpoint, written in response to this same failure shape, sitting in a
  release that was never rolled out
- a deployment manifest that declares the correct probe, against a cluster running a binary
  two releases older — applied by hand, field by field
- complete metrics that nothing scrapes
- 38 alerting rules, none of which mention this system
- an alert count dropping to zero because reads stopped working entirely

Every one of those is a detection failure, and detection failure is what converted a
recoverable bug into 31 days. The fix in `20d1f3e` prevents this specific state. Items 1–5
are what prevent the *next* unknown bug from lasting a month.

The uncomfortable version: the previous incident produced a correct diagnosis, a correct
code fix, and a correct manifest change — and none of it reached production. Writing the
remediation is evidently not the hard part.
