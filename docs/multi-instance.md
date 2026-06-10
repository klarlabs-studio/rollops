# Multi-Instance Coordination

Rollops can run more than one daemon against the same runtime Store. The Store
remains runtime-only: Git is still the only desired-state source, and leases only
coordinate who may act.

## Target Leases

Target-mutating engine operations take two locks:

- an in-process advisory lock for local concurrency
- a Store lease named `target:<target-ref>` when the Store supports leases

SQLite supports leases out of the box. A contended target lease returns
`ErrTargetBusy`, so another daemon or one-shot command does not deploy over an
active rollout. Leases have a TTL and are stealable after expiry, so a crashed
process cannot block the target forever.

## Reconcile Leader

The Git watcher can use a Store lease named `reconcile:leader`. The leader
renews this lease on each tick; non-leaders skip reconciliation with
`ErrNotLeader`. If the leader dies, another instance can acquire the lease after
the TTL.

Daemon defaults:

- `ROLLOPS_INSTANCE_ID`: owner id for target and reconcile leases. Defaults to
  `rollopsd`.
- Lease TTL: two minutes.

This is enough for active/passive daemon redundancy on the same SQLite file or a
future shared Store backend without introducing Kubernetes or a separate
coordination service.
