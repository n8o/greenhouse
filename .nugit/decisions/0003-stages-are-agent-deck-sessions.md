---
schema_version: 1
id: ADR-0003
type: decision
scope: global
status: proposed
created: 2026-09-29T00:00:00Z
relates_to:
  - constrains:launch
provenance:
  commit: bootstrap
  citation: docs/DESIGN.md
confidence: medium
---

# ADR-0003 — Every stage is an agent-deck session

## Context
A factory's sessions need to be visible, attachable when one goes wrong,
stamped on the PRs they open, and able to report completion. agent-deck
already does all of this: `launch -worktree`, the `===AGENTDECK_DONE===`
sentinel, `session children --json`, the inbox, `remote` for another host, and the
PR-stamping convention a customer repo relies on.

## Decision
`internal/launch` shells out to `agent-deck launch <repo> -worktree <branch>
-b -c claude -g greenhouse -assert-done` with the rendered stage prompt, and
learns of completion from `agent-deck session children --json` / `inbox
drain`. greenhouse keeps no session process table of its own.

## Consequences
- The human can `agent-deck` into any running stage.
- greenhouse depends on agent-deck's CLI contract; `greenhouse doctor` pins the
  minimum version.

### Rejected alternatives
- **Raw `claude -p` subprocesses.** No attach, no remote, no stamping, and a
  second process manager to maintain.
