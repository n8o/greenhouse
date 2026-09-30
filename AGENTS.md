# AGENTS.md — working in greenhouse

greenhouse is a software factory that fits within one person's Claude Max
subscription. Read [docs/DESIGN.md](docs/DESIGN.md) first. The ADRs in
`.nugit/decisions/` are the constraints; their **rejected alternatives are
settled, so do not re-propose them.**

## The plan builds this repo

The product plan is `.beads/plans/gr-boot.jsonl`. Steps up to **gr-boot-6**
are built by hand, in interactive sessions. From gr-boot-6 on, greenhouse
runs its own remaining steps. Work on one step at a time; close it only when
its "Done when" holds.

```sh
bd ready                                   # what is unblocked
bd update gr-boot-N --status in_progress   # local only, never committed
bd close  gr-boot-N --reason "this PR"     # in your step's PR, just before ready
scripts/beads-export.sh                    # regenerate .beads/plans/ — never hand-edit it
```

**Keep commits cheap (ADR-0005).** Git carries the plan, not the run:
- Never commit a bead's in-progress state or stage labels; the export strips them.
- Your step's PR closes its own bead. Merge means closed; there are no follow-up bead commits.
- Your PR changes only your bead's line in `.beads/plans/`. If the export
  touched other lines (another session's local state), restore them with
  `git checkout -- <file>` and re-add only your line.

## Build / test

```sh
go build ./... && go test ./...
go vet ./... && gofmt -l .                 # must be clean
nugit pr-render -base main -head HEAD      # must have no fail findings
```

## Rules

1. **No LLM in the control plane** (ADR-0001). `internal/stage` is pure: no I/O,
   no clock, no randomness. The only thing that calls Claude is a stage session
   launched through agent-deck (ADR-0003).
2. **Shell out, don't reimplement.** Beads goes through `bd`, sessions through
   `agent-deck`, PRs through `gh`, and knowledge and verification through
   `nugit`. Parse their `--json` output, and put that parsing behind one small
   adapter per tool so tests can fake it.
3. **Keep `workspace.dsl` in sync with the import graph.** A new cross-package
   import needs its `src -> dst` edge; a new package needs a component.
4. **Load context before non-trivial edits:**
   `nugit context -path <file> -task "<task>" -budget 12000`.
5. **Commit trailers** (`symptom:`, `decision:`, `rejected:`, `learned:`,
   `affects:`, `keywords:`) when a change carries a decision. Durable decisions
   become `.nugit/decisions/` files.
6. **Draft while in progress, ready once validated; a human merges.** Open a PR
   as a draft. Mark it ready (`gh pr ready`) only when it is dev complete and
   every verify command passes. Ready means "validated, review me". Never
   merge, never approve.
