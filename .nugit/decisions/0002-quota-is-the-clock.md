---
schema_version: 1
id: ADR-0002
type: decision
scope: global
status: proposed
created: 2026-09-29T00:00:00Z
relates_to:
  - constrains:gate
provenance:
  commit: bootstrap
  citation: docs/DESIGN.md
confidence: medium
---

# ADR-0002 — Quota is the clock

## Context
On a Claude Max subscription the binding constraint is the rolling 5-hour and
7-day usage windows, not dollars. A factory that hits the limit also locks the
human out of their own interactive work until the window resets.

## Decision
Before every launch the gate reads `agent-deck usage --json`, which caches the
`rate_limits` Claude Code's statusLine pushes via `agent-deck usage ingest
claude`. It launches only when 5-hour usage is below `quota.five_hour_max` and
7-day usage is below `quota.weekly_share` (default 0.5, which leaves half the
week to the human). If the reading is missing or stale, the gate falls back to
reactive mode: a session that reports a usage-limit stop parks the scheduler
until the reported reset time.

## Consequences
- Throughput is bounded by design. Raise `weekly_share` deliberately, never by
  accident.
- The statusLine must be wired through `agent-deck usage ingest claude` on the
  host that runs greenhouse (checked by `greenhouse doctor`).

### Rejected alternatives
- **Fixed launches per hour.** Simple, but blind to how expensive each stage
  actually is.
- **API-key billing.** It would remove the windows but change the cost model
  the project exists to fit.
