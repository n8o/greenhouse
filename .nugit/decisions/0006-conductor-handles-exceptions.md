---
schema_version: 1
id: ADR-0006
type: decision
scope: global
status: proposed
created: 2026-10-10T00:00:00Z
relates_to:
  - constrains:cli
  - constrains:launch
  - constrains:doctor
provenance:
  commit: bootstrap
  citation: docs/DESIGN.md; agent-deck conductor/notify-daemon/inbox CLI help
confidence: medium
---

# ADR-0006 — The scheduler handles the routine; a conductor handles the exceptions

## Context
ADR-0001 keeps judgment out of the scheduler, so anything unexpected becomes
`stage:parked` with a reason. A parked bead then waits until a human happens
to run `greenhouse status`. agent-deck already has an LLM supervisor, the
**conductor**: a long-lived session woken on a delta-only heartbeat, which can
queue tiered messages for the human (`conductor notify --tier urgent|info`)
through a notification bridge.

agent-deck's **notify-daemon** records session transitions into durable
inboxes (`inbox drain`, `remote drain`). The scheduler does not need that
history: a finished session's final message stays readable until it is
collected.

## Decision
1. **The scheduler does not depend on the notify-daemon.** Each tick polls
   `agent-deck session children --json` for state. When a session is finished,
   it reads that session's final message, parses `GREENHOUSE-EVENT` strictly,
   writes the result to the ledger, and stops the session. `greenhouse doctor`
   reports a missing daemon as a **warning**: optional, used for laptop
   notifications via `remote drain`.
2. **A conductor is the exception handler, not the router.** One agent-deck
   conductor per greenhouse host watches parked beads and the ledger. For
   every park it notifies the human (urgent for needs-human, info for
   digests) with the question and, where it can, a drafted answer. It never
   moves a bead, merges, approves or launches stage sessions. Unparking stays
   a human act.
3. **Tokens go to surprises only.** The routine path costs nothing between
   stage sessions. The conductor's heartbeat pauses when idle
   (`-heartbeat-idle-minutes`) and wakes on deltas only.

## Consequences
- One less service on the critical path: greenhouse runs without the daemon.
- The human learns about a park within one heartbeat, not whenever they next
  look.
- The conductor's instructions are a versioned file in this repo, so changes
  to how exceptions are handled get reviewed like code.

### Rejected alternatives
- **Making the conductor the scheduler.** Already rejected in ADR-0001: an LLM
  would decide routine routing, burning tokens on every heartbeat.
- **Requiring the notify-daemon for completion events.** It adds a
  must-be-running service to collect history the scheduler can read directly.
- **Notifying from the scheduler itself (email, Slack).** It would duplicate
  agent-deck's bridge and tiers, and drafting an answer to a park needs
  judgment anyway.
