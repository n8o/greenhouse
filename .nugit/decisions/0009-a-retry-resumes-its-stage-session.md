---
schema_version: 1
id: ADR-0009
type: decision
scope: global
status: proposed
created: 2026-10-16T00:00:00Z
relates_to:
  - amends:ADR-0003
  - informs:ADR-0007
  - constrains:launch
  - constrains:cli
  - constrains:ledger
provenance:
  commit: fix/retry-reuses-session
  citation: internal/launch/launch.go, cmd/greenhouse/tick.go, prompts/retry.md
confidence: medium
---

# ADR-0009 — A retry resumes its stage's session

## Context
A stage session's title is deterministic (`gh-<stage>-<bead>`) and agent-deck
refuses a second session with a title that is taken. A collected session is
stopped, not removed, so it keeps its title. Every second run of a stage
therefore failed to launch: a verify failure sent the bead back to implement,
the launch collided with the stopped implement session, and the bead parked
as needs-human. A spec relaunched after a usage-limit park, and a spec after
an unpark, collided the same way.

## Decision
1. **A stage run whose session still exists continues that session.** The
   scheduler looks the title up (`agent-deck list -json`). If a session holds
   it, the run reuses it: start it again if it is stopped or errored
   (`agent-deck session start`, which resumes the Claude conversation), then
   send it `prompts/retry.md` with why it runs again (the verify log, the
   usage-limit reset, or the bead's last outcome) and confirm delivery with
   the same check-and-resend-once as a launch. A session that is already
   working is adopted without a send.
2. **The previous completion never counts.** Before anything is sent, the
   open run records the session's last reply (its transcript timestamp and a
   content digest) and how many ledger records the session already has.
   Delivery and collection accept only a reply written after that reply; a
   session idle with the old reply is still waiting, bounded by the stage's
   wall-clock limit. The run is recorded once the session has one more
   ledger record than it had.
3. **The open run is written before the send, and marked sent after.** A
   tick that crashes between them redoes the send, or adopts the session if
   it is already working; the baseline taken before the first send is kept.
4. **No session, launch as before.** If a human removed the session, the
   retry launches a new one on the existing branch (no `-b`).
5. Retry limits are unchanged: `stage.MaxVerifyRetries` still parks a bead
   whose verify fails again, and the ledger's `retries` counts the same way.

## Consequences
- An implement retry keeps its context, worktree and branch, and does not
  pay to re-read the repo.
- One agent-deck session holds every run of its stage for a bead, so a human
  attaching sees the whole history. The ledger has one record per run, all
  with the same session id.
- Collection depends on agent-deck's `session output -json` timestamp; with
  none, a reply identical to the previous one is not seen as new and the run
  times out instead of being misread.
- Unparking still restarts the bead at spec (ADR-0007), now in its existing
  spec session, which is told to re-read the bead.

### Rejected alternatives
- **Unique titles per attempt (`gh-implement-<bead>-2`).** Loses the
  session's context, re-reads the whole repo, and leaves orphan sessions and
  worktrees on the same branch.
- **Remove the old session, then launch.** Destroys the history a human can
  attach to.
