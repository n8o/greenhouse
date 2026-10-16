---
schema_version: 1
id: ADR-0010
type: decision
scope: global
status: proposed
created: 2026-10-16T00:00:00Z
relates_to:
  - amends:ADR-0007
  - informs:ADR-0005
  - informs:ADR-0009
  - constrains:cli
  - constrains:plan
  - constrains:ledger
  - constrains:stage
provenance:
  commit: fix/unpark-resumes-the-parked-stage
  citation: cmd/greenhouse/tick.go, internal/plan/plan.go, internal/stage/stage.go, internal/ledger/ledger.go
confidence: medium
---

# ADR-0010 — "Done" is closed on main, and an unpark resumes the stage that parked

## Context
ADR-0007 rule 4 read "done" from the local bd database: closed, with no
stage label or `stage:done`. Rule 5 restarted an unparked bead at spec.
In a real run, the two rules combined into one failure:

1. gr-boot-7's implement session closed its bead in the shared local bd
   database (`close_reason: "this PR"`) while preparing its PR's plan line,
   as ADR-0005 tells a step's PR to do.
2. Verify failed, the retry collided with the stopped session (fixed since
   by ADR-0009), and the bead parked.
3. The human removed `stage:parked`, the documented unpark.
4. The bead was now `closed` with no stage label, which is exactly rule 4's
   "done". The next tick started gr-boot-8, which depends on gr-boot-7, on
   top of an unmerged, unverified PR. The operator recovered by setting
   `stage:verify` by hand.

The local bd database is shared with every stage session, so an in-flight
PR's agent changes it before merge. Only git says what has merged.

## Decision
1. **Done means closed on the default branch.** A bead is done, for itself
   and for the dependents it blocks, when its line is `closed` in the plan
   export (`.beads/plans/*.jsonl`) at `origin/main`. Each tick fetches origin
   and reads that export (`git ls-tree` and `git show`). If the fetch fails,
   the tick reads the ref as last fetched, which shows fewer beads done,
   never more. If no plan can be read there, the tick fails and changes
   nothing. The local status still gates *starting* a bead, which must be
   open, but it never makes a bead done.
2. **A bead closed locally whose close is not on main is not done.** The
   scheduler says so on every tick, and a dependent's "waits on" line names
   it. With no stage label:
   - if the ledger has runs of it, it is treated as an unparked in-flight
     bead and resumes (rule 3);
   - otherwise an approved bead is parked with the reason (a
     `stage: parked`, `needs-human` ledger record): merge the PR that closes
     it, or reopen it and unpark it. A bead that is not approved is only
     reported, since greenhouse does not move beads it was not given
     (ADR-0004).
3. **An unpark resumes the stage that parked.** Removing `stage:parked`
   resumes the stage the bead's last stage run implies (`stage.Resume`). A
   spec, implement or learn run that parked runs again, in its existing
   session (ADR-0009: a send, not a launch). A verify run that parked with
   its retries used is verified again. A rejected PR's finished lesson
   resumes at spec, since it asked for a re-plan. A run that did not park
   (a lost label) resumes where its transition leads. A merged bead does
   not resume; it is parked for a human.
4. **Choosing the stage is a label.** Adding `stage:<stage>` next to
   `stage:parked` resumes the bead there, and the scheduler then makes that
   label the only one. `stage:spec` is the explicit restart from scratch.
   Add the label rather than replace `stage:parked`: a label equal to the
   stage of the run that parked the bead, with no `stage:parked` beside it,
   is how a transition the scheduler left unfinished looks (ADR-0007 rule
   1), and it is finished, not released.
5. **An unpark is a ledger record.** It has `stage: parked`, `outcome:
   unparked` and `resume: <stage>`, and it is appended before the label
   moves (ADR-0007 rule 1), so a crash in between finishes the move without
   recording the unpark twice. It resets the verify retry budget
   (`ledger.Retries` counts from the last spec run *or unpark*), because a
   human has looked at the bead. A resumed session is told that a human
   released it and why it parked.

This replaces ADR-0007 rules 4 and 5. Its other rules hold.

### Choosing the source of "done"
The alternative was "closed locally and the ledger has a review → merged
record for it". It is rejected:

- It would make the ledger, which is local and gitignored, the authority
  on completion. ADR-0005 makes git the authority: a step closes in its own
  PR, and merge means closed.
- Every bead finished before the ledger existed (gr-boot-1 to gr-boot-6,
  built by hand), and every bead merged on another host, has no merged
  record, so none of them could unblock a dependent.
- It still trusts a local `closed`, which is the field that was wrong.

Main's export is the record ADR-0005 already writes. It needs no new
state, and every clone of the repo agrees on it.

## Consequences
- A dependent starts one fetch after its blocker's PR merges, not when an
  agent closes the blocker locally.
- A tick costs one `git fetch` and one `git show` per plan file. Offline,
  it is conservative.
- The ledger now holds two kinds of record that are not stage runs (an
  unpark and a scheduler park), both with `stage: parked`.
  `ledger.LastRun` skips them, and `ledger.Last` sees them.
- Starting over is a deliberate act (`stage:spec`), not the side effect of
  every unpark.

### Rejected alternatives
- **Closed locally plus a ledger `merged` record.** See above.
- **Keep restarting at spec on unpark.** That throws away a verified spec
  and an open PR whenever verify, a question or a launch problem parks a
  bead, and it pays for a new spec session to recover from a failure that
  was not the spec's.
- **A `greenhouse unpark <bead> [stage]` command.** It is more to build
  than the label it would set, and a human can already set labels with
  `bd label`.
- **Counting a bead closed locally with no history as done anyway.** That
  is the original bug.
