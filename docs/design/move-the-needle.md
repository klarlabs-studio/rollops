# RFC: Move the needle

Status: **Executed** — Phases A–C verified in Roady.
Date: 2026-08-15.
Companion: `.roady/spec.yaml` features `needle-history-surface`,
`needle-walkthrough`, `needle-example-bar`.

## Problem

v0.32.0 shipped the agent loop as a product. Strangers still cannot *feel*
why Rollops is beyond Argo: the plan says `needs_approval` but not that
**two recent rollbacks** drove it, and there is no single narrative page that
walks Claude/Cursor from token → escalate → human approve → history.

agent-go vulns, cosign on the live fleet, and PAT rotation stay
**out of this repo**. This program only moves what Git can move.

## Non-goals

- Forced cosign / forced `risk.history` on every install
- Obvia analysis default-on, ApplicationSet matrix/git, host agent, Studio
- Work in agent-go / nox / relicta
- New MCP verbs beyond legible risk fields

## Phases

### Phase A — History legible on plan

Expose `recent_failures` (count inside `risk.history` lookback) on
`Engine.Plan` and MCP/HTTP/gRPC/CLI plan responses when history is configured.
Agents escalate with a reason they can cite, not a boolean mystery.

Acceptance: seeded rolled-back history → plan returns `recent_failures >= 1`
and a raised score; no history config → field stays 0.

### Phase B — Public proof walkthrough

`docs/agent-walkthrough.md`: one narrative from MCP enable → plan JSON with
`needs_approval` + `recent_failures` → do not apply → human approve → canary
controls → `rollouts.history` attribution. Linked from README and
agent-operator as the proof page.

### Phase C — Agent-grade example bar

Examples that already declare `risk` include `history` + a sensitive CEL that
references `recentFailures` (and schema). Hetero + rollout-config.example
match the walkthrough. Honesty: still opt-in in the schema; examples teach the
bar.

## Done when

Roady tasks verified; walkthrough is the README USP link; plan surfaces
`recent_failures` under tests.

Not done when: more Argo checkboxes.
