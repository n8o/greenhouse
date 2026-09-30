---
schema_version: 1
id: ADR-0004
type: decision
scope: global
status: proposed
created: 2026-09-29T00:00:00Z
relates_to:
  - constrains:plan
  - constrains:verify
provenance:
  commit: bootstrap
  citation: docs/DESIGN.md
confidence: medium
---

# ADR-0004 — Intake is approved beads only; verification is deterministic; a human merges

## Context
An agent with repo write access that reads issue text or PR comments can be
steered by whoever wrote them (prompt injection). An LLM grading its own work
is not a gate. A factory that merges on its own turns every one of its
mistakes into main.

## Decision
- **Intake:** only beads carrying `factory:approved`, applied by the human.
  Nothing else becomes a stage prompt.
- **Verify:** only commands from `greenhouse.toml` (build, tests, `nugit
  pr-render -fail-on fail`). On failure, the log goes back to implement once;
  after that the bead is parked.
- **Draft → ready → merge:** implement opens the PR as a draft. Only a verify
  pass marks it ready (`gh pr ready`), so a ready PR always means dev complete
  and validated. The human reviews and merges ready PRs. greenhouse
  never merges, and it never approves.
- **Backlog cap:** no new implement launches while `wip.open_prs` agent PRs are
  open.

## Consequences
- The human is the throughput ceiling, deliberately. The ledger shows when that
  is the bottleneck.

### Rejected alternatives
- **GitHub-issue or Slack intake.** Deferred: it brings untrusted text in and
  needs a sanitising triage stage first.
- **A merge queue (a Bors-style Refinery).** Rejected: it removes the human
  from main.
