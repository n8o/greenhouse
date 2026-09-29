---
schema_version: 1
id: ADR-0001
type: decision
scope: global
status: proposed
created: 2026-09-29T00:00:00Z
relates_to:
  - constrains:cli
  - constrains:stage
  - constrains:gate
provenance:
  commit: bootstrap
  citation: docs/DESIGN.md
confidence: medium
---

# ADR-0001 — The control plane uses no LLM

## Context
Multi-agent orchestrators such as Gas Town put an LLM in the coordinator seat
(Mayor, Witness, Deacon), and those roles think continuously, idle or not. That
is where their token burn comes from. greenhouse runs on one Claude Max
subscription, where every idle thought uses quota the human needs for their own
work.

## Decision
The scheduler (`greenhouse tick` / `run`) is deterministic Go. Picking the next
bead, deciding the next stage, gating, retrying and parking are all plain code
over Beads labels, agent-deck session state and `gh pr list`. Claude is called
only inside stage sessions, once per stage run, with a turn budget.
`internal/stage` is a pure function (stage, event) → (next stage, action) with
no I/O, so it is fully unit-testable.

## Consequences
- Zero token cost while idle; the cost of a bead is the sum of its stage runs,
  and the ledger records it.
- No "judgment" in routing. Anything that needs judgment is a stage session or
  goes to the human (`stage:parked`).

### Rejected alternatives
- **An LLM foreman/conductor agent.** It handles fuzzy situations more
  flexibly, but it burns tokens continuously and makes routing
  non-reproducible.
- **Gas Town.** Rejected for its token burn and because its Refinery
  auto-merges.
