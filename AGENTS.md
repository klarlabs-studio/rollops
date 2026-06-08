# Rolloffs — Project Identity

*Rollout operations for the agentic web.* Lean, infrastructure-agnostic rollout
orchestration where **agents and humans are peer operators**. Leaner alternative
to ArgoCD/Flux; no Kubernetes dependency; runs on a bare Hetzner VPS.

- **Module:** `github.com/klarlabs/rolloffs` (repo dir is `rollops`)
- **Language:** Go 1.26
- **Umbrella:** Klarlatz · **Brand DNA:** Smart, Präzise, Wertig, Verlässlich
- **Canonical docs:** `rolloffs-vision.md`, `rolloffs-tdd.md`

## Working Style

- **TDD, red-green-refactor.** Test first; table-driven Go tests. Target the
  conformance suite for every Target implementation.
- **Atomic conventional commits** (`feat:`, `fix:`, `test:`, `docs:`…).
- **Solution-first.** Fix root causes; no workarounds except for external/3rd-party blocks.
- **Packages by domain, not layer.** Keep functions small, names self-documenting.
- **Lean is a feature.** Every addition justifies its weight. Single binary + SQLite is the common case.
- Plan through **Roady** before implementing; capture sessions to `memory/`.

## Constraints (from vision/TDD)

- **No Kubernetes dependency** in core. K8s is one target plugin, never an assumption.
- **Observability-free risk + rollback in v1.** Metric-based analysis is Phase 2 (Obvia seam) — do not bake it in.
- **Not coupled to Obvia / Relicta.** decision-kit used directly; relicta's DSL stays separate.
- **CEL for conditional logic**, not a bespoke DSL. YAML surface + strict published schema + loud Go validation.
- **Git is the only source of desired state.** The Store holds runtime state only — never desired.
- **Secrets never stored locally.** Vault via `SecretProvider`; redact at the audit boundary (bolt).
- **Every interface authenticated** (mTLS/signed tokens), including MCP. RBAC on operations.
- **Agent guardrails are hard limits**, not advisory: non-bypassable policy floor, kill-switch, full attribution, fortify rate-limit.
- **Build on the existing stack** — don't reinvent. Pinned deps (see `internal/stack/stack.go`):
  `go.klarlabs.de/{statekit,axi,fortify,bolt,mcp,mnemos}` + `github.com/felixgeelhaar/decisionkit` (risk gate → `/risk`). Note: `axi`/`mcp`, not `-go`.

## Known Failure Modes

- Re-introducing observability assumptions into v1 risk/rollback. Keep them out.
- Treating the Store as desired-state truth. It is not — Git is. Losing the Store must never corrupt what should be deployed.
- Target implementations that aren't idempotent or have unstable fingerprints → conformance suite must catch this.
- Leaking secret material into plans, diffs, logs, or MCP responses.
- Letting the daemon become a single point of failure — one-shot in-process path must stay behaviourally identical.

## Open Items (TDD §17)

Config schema v1 + versioning · plugin gRPC protocol · metric-analysis interface (P2) ·
multi-instance coordination · UI act-vs-observe scope · concrete RBAC taxonomy.
