---
schema_version: 1
id: ADR-0007
type: decision
scope: global
status: proposed
created: 2026-10-11T00:00:00Z
relates_to:
  - constrains:cli
  - constrains:ledger
  - constrains:plan
provenance:
  commit: gr-boot-6
  citation: cmd/greenhouse/tick.go, internal/ledger/ledger.go
confidence: medium
---

# ADR-0007 — The ledger is the scheduler's commit point

## Context
`greenhouse tick` must be idempotent and crash-safe (gr-boot-6): killed at
any point, the next tick converges and no action happens twice. A tick
touches four systems (bd labels, agent-deck sessions, gh PRs, and
`.greenhouse/`), and none of them is transactional with the others.

## Decision
1. **One ledger line per stage run, appended when its outcome is known.**
   The line holds the outcome as a stage event. The scheduler then carries
   out the transition `stage.Next` returns. Every effect is checked or safe
   to repeat (label the PR, `gh pr ready` only on a draft, close only an
   open bead), and the bead's stage label moves **last**. While the label
   still equals the stage of the bead's last record, the transition is
   unfinished and the next tick redoes it.
2. **An open-run file comes before every launch.** `.greenhouse/runs/<bead>.json`
   is written before `agent-deck launch` and deleted after the run's ledger
   line. A session found for an open run without a recorded session id is
   adopted, not launched again.
3. **Verify runs in a scheduler-owned worktree** (`.greenhouse/verify/<bead>`):
   the PR head, rebased onto `origin/main` locally, never pushed. A rebase
   conflict is a verify failure.
4. **"Done" means closed and not in flight.** An implement session closes
   its bead inside its PR (ADR-0005), so a closed bead with a stage label
   other than `stage:done` is still moving and does not unblock dependents.
5. **Unparking is removing `stage:parked`.** The bead restarts at spec as a
   fresh start, the same as `stage.Unparked`.

## Consequences
- Retry counts, PR numbers and park reasons are read back from the ledger;
  nothing about a run is stored in bd beyond the stage label.
- A torn final ledger line is skipped: that run was never committed and is
  collected again.

### Rejected alternatives
- **A start line and an end line per run.** It doubles the ledger and still
  needs a commit rule for the end line; the open-run file covers the start.
- **Rebasing and force-pushing the PR branch from verify.** The scheduler
  would write to an agent's branch; verify only reads.
- **Recording run state in bd notes or comments.** They export to git
  (ADR-0005).
