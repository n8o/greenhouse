---
schema_version: 1
id: ADR-0005
type: decision
scope: global
status: proposed
created: 2026-09-30T00:00:00Z
relates_to:
  - constrains:plan
  - constrains:stage
  - constrains:ledger
  - constrains:verify
provenance:
  commit: bootstrap
  citation: scripts/beads-export.sh, .beads/plans/gr-boot.jsonl history (gr-boot-1, gr-boot-2)
confidence: medium
---

# ADR-0005 — Git carries the plan, not the run

## Context
A factory that keeps its state in git can turn every state change into a
commit. Every commit touching a shared file is a merge conflict waiting for
two concurrent sessions. We saw this in the first two steps:

- Each step produced separate `chore(beads): gr-boot-N in progress` and
  close commits, in addition to the work itself.
- `bd export` serializes the whole local database, so one session's export
  sweeps in another session's changes.
- `updated_at` changes on every `bd update`, so even a no-op edit rewrites the
  line.

A customer repo using the same Beads-in-git pattern hit exactly this: two
sessions closing two different steps produced whole-file rewrites git cannot
merge, and the conflict landed on people whose change had nothing to do with
the plan.

## Decision
1. **Git holds only durable facts:** the plan (steps, "Done when",
   dependencies) and completion (closed, with the reason). **Run state never
   enters git.** That covers `stage:*` labels, `in_progress`, retry counts,
   session ids and the ledger. It lives in the local bd database and
   `.greenhouse/` (both gitignored) and can be rebuilt from agent-deck, `gh`
   and the ledger.
2. **The export strips run state.** `scripts/beads-export.sh` drops `stage:*`
   labels, writes `in_progress` as `open`, and drops `updated_at`. The
   exported line for a bead therefore changes only when its plan or its
   completion changes.
3. **A step closes in its own PR.** The PR that completes gr-boot-N carries
   that bead's line flipped to closed (`close_reason: "this PR"`). Merge means
   closed; a rejected PR means the bead stays open. There are no follow-up
   bead commits and no "in progress" commits.
4. **One PR, one bead line.** A stage session's PR may change only its own
   bead's line in `.beads/plans/`. The verify stage enforces this
   deterministically (gr-boot-5). Plan edits (new steps, re-scoping) are
   separate human PRs.
5. **One stable line per bead, one file per plan** (`nugit plan normalize
   -split`), so concurrent steps produce disjoint hunks that git merges
   without help.
6. **Nothing derivable is committed.** Generated indexes and reports are
   rebuilt, not merged.

## Consequences
- A step's PR shows the work and the plan-position change together
  (`nugit pr-render`: "Changes since base").
- "Which step is in flight" is no longer visible in git. `greenhouse status`
  (local state) is the place to look.
- Two stage sessions can run concurrently without touching the same lines.

### Rejected alternatives
- **`merge=union` on the JSONL.** When two branches change the same bead,
  union keeps both versions of its line: a duplicated bead that is silently
  wrong.
- **A separate state branch or a Dolt remote for run state.** It is a second
  sync system to operate, for data that can be rebuilt anyway.
- **Committing in-progress status for visibility.** That visibility costs a
  commit, and a potential conflict, on every transition.
