---
schema_version: 1
id: ADR-0008
type: decision
scope: global
status: proposed
created: 2026-10-16T00:00:00Z
relates_to:
  - amends:ADR-0005
  - constrains:verify
  - constrains:plan
provenance:
  commit: fix/plan-scope-own-spec
  citation: internal/verify/scope.go, prompts/spec.md
confidence: medium
---

# ADR-0008 — A bead's PR carries its own spec

## Context
ADR-0005 rule 4 lets a PR change only its own bead's line under
`.beads/plans/`, and only to close it. The verify stage enforces that: any
field other than the close fields (`status`, `close_reason`, `closed_at`,
`closed_by`, `started_at`) counts as a plan edit and fails the check.

The spec stage records its output on the bead with
`bd update <bead> --acceptance "..." --design "..."`, which sets
`acceptance_criteria` and `design`. The export then carries both fields into
the implement PR's line for that bead. gr-boot-7's implement PR failed
verify with:

> plan-scope failed: gr-boot-7: plan fields changed: acceptance_criteria,
> design — A PR may change only the line of its own bead under .beads/plans/,
> and only to close it (ADR-0005)

So the factory's own spec stage made every PR fail verify.

## Decision
A PR may also add or change the **spec fields on its own bead's line**:
`acceptance_criteria` and `design` (what the spec stage writes) and `notes`.
Everything else in ADR-0005 holds:

- Other beads' lines must be untouched, spec fields included.
- On its own line, the PR must still close the bead. Any field other than the
  close fields and the spec fields (title, description, priority,
  dependencies, ...) still fails the check.
- A bead that was already closed still may not change.
- Run state (`stage:*` labels, `in_progress`, `updated_at`) is still never
  committed.

ADR-0005 cares about two things: merge conflicts, and run state in git.
Neither applies here. Only the bead's own PR touches its line, so the spec
cannot conflict with another session. And the spec is plan, not run state: it
records what was agreed to build.

## Consequences
- The merged PR shows the work next to the spec it was held to, so the
  reviewer can check one against the other.
- A spec revised by a later run (a retry after a parked bead) lands in that
  bead's own PR too.
- Re-scoping a bead (title, description, dependencies, priority) is still a
  separate human PR.

### Rejected alternatives
- **Strip the spec fields in the export.** The agreed spec would never reach
  git, and the reviewer could not see what the PR was held to.
- **Have the spec stage write to a side file.** That makes a second source of
  truth for the plan, next to the bead.
