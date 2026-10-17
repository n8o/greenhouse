---
schema_version: 1
id: ADR-0012
type: decision
scope: global
status: proposed
created: 2026-10-17T00:00:00Z
relates_to:
  - amends:ADR-0011
  - constrains:cli
  - constrains:doctor
  - constrains:launch
provenance:
  commit: fix/sessions-start-on-base
  citation: cmd/greenhouse/tick.go, internal/doctor/doctor.go, internal/launch/preflight.go
confidence: medium
---

# ADR-0012 — Stage branches start from the base, and doctor reads the base

## Context
ADR-0011 made the base branch configurable, but two things still did not
follow it:

1. A new stage session's branch was made by `agent-deck launch -worktree
   <branch> -b`, which has no option for where the branch starts. Measured
   with throwaway repos, agent-deck branches from origin's default branch,
   freshly fetched, whatever the checkout has checked out. With no remote,
   it branches from the checked-out branch. So `base` was ignored whenever it
   differed from origin's default.
2. `greenhouse doctor`'s MCP preflight read `.mcp.json` and
   `.claude/settings.json` from the checkout's working tree. On a checkout
   left on an old feature branch, it reported an MCP approval as missing,
   although the base branch every session starts from had it.

## Decision
- Before launching a stage on a branch that does not exist yet, the
  scheduler runs `git branch --no-track <branch> origin/<base>`. Each tick
  fetches origin first, so the base is fresh. It then launches agent-deck on
  that existing branch, without `-b`. If the branch cannot be created, the
  tick fails and keeps the open run, so the next tick tries again. The bead
  is not parked. A dry run says where the branch would start and creates
  nothing.
- doctor's MCP check reads both files from `origin/<base>` with `git show`.
  It falls back to the working tree only when the base check failed.

## Rejected alternatives
- **Keep relying on agent-deck's choice of start point.** It is right only
  while `base` is origin's default branch. It is also undocumented: the
  start point was found by measuring, not from a contract.
- **Check out the base in the working tree before launching.** That changes
  the checkout a human may be working in, and it does not help a session
  whose worktree is created elsewhere.

## Consequences
- A stage session starts from the base the config names, on any repo.
- Which branch the working checkout has out no longer matters, to the
  scheduler or to doctor.
