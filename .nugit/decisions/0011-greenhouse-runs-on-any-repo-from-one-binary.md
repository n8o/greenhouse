---
schema_version: 1
id: ADR-0011
type: decision
scope: global
status: proposed
created: 2026-10-17T00:00:00Z
relates_to:
  - amends:ADR-0010
  - constrains:cli
  - constrains:config
  - constrains:launch
  - constrains:doctor
  - constrains:prompts
provenance:
  commit: feat/any-repo
  citation: cmd/greenhouse/main.go, cmd/greenhouse/init.go, cmd/greenhouse/lock.go, internal/config/config.go, internal/launch/launch.go, internal/doctor/doctor.go, prompts/embed.go
confidence: medium
---

# ADR-0011 — greenhouse runs on any repo from one binary

## Context
greenhouse is meant to run a plan on any repo, and it is public, so anyone
should be able to. Until now it only worked on its own checkout:

1. The base branch was a constant, `origin/main`. On a repo whose default
   branch is `master`, every tick failed to read the plan, and verify would
   rebase onto a ref that does not exist.
2. Stage prompts were read from `<repo>/prompts/`, the repo greenhouse works
   on. Any other repo has no such directory, so launching a stage would fail.
3. The prompts pointed sessions at greenhouse's own `AGENTS.md` "Build /
   test" section and its ADR numbers.
4. Run state is written to `<repo>/.greenhouse/`. greenhouse's `.gitignore`
   covers it; another repo shows it as untracked files.
5. Running it at all meant cloning greenhouse, because of (2) and the example
   config.

## Decision
- **`base` config key.** It names the branch PRs merge into. Unset, it is
  the branch `origin/HEAD` points at, which `git clone` records. If neither
  exists, the scheduler refuses to start and says to set `base`. There is no
  `main` fallback, since a wrong base reads the wrong plan and rebases onto
  the wrong ref. This amends ADR-0010: "done" means closed on `origin/<base>`.
- **Prompts are embedded** (`prompts/embed.go`, `go:embed`). A config's
  `prompts` key names a directory that replaces them wholesale, and doctor
  renders every template from it. They tell a session to read the repo's own
  agent instructions (AGENTS.md or CLAUDE.md) for build and test commands.
- **Run state stays out of `git status`.** On a real (non-dry) tick or run,
  greenhouse appends `/.greenhouse/` to the clone's `info/exclude`, unless
  the repo already ignores it. It never edits a tracked file of a repo it
  does not own.
- **`greenhouse init`** writes a starter config for a repo (`-repo`, `-plan`,
  `-base`, `-config`). The config validates as written, and its only verify
  command is `nugit pr-render` against the base, until the repo's own checks
  are added. **`greenhouse version`** prints the build's module version.
- **New doctor checks:**
  - `base`: `origin/<base>` exists, or origin has a default branch.
  - `prompts`: every stage template renders.
  - `export`: the repo has `scripts/beads-export.sh`. Missing, it is a
    warning: implement sessions then cannot close their bead in the plan.

## Rejected alternatives
- **Fall back to `main` when `origin/HEAD` is unset.** It is silently wrong
  on any `master` or `trunk` repo. An explicit `base` costs one line.
- **Keep prompts in the target repo** (`greenhouse init` copying them in).
  Every repo would carry a fork of the prompts that drifts from the binary,
  and greenhouse would commit files into repos it does not own.
- **Move run state to `$(git-common-dir)/greenhouse/`.** It would hide the
  state without touching `info/exclude`, but it moves the ledger of every
  existing install and changes the path `report` and the docs use. The
  exclude is a one-line, local, idempotent change.
- **Ship an export script inside the binary.** The export depends on the
  repo's plan layout and its nugit version. Each repo owns its script; doctor
  makes the requirement visible.

## Consequences
- `go install github.com/n8o/greenhouse/cmd/greenhouse@latest` plus
  `greenhouse init` is a complete install. The module has no private
  dependencies.
- greenhouse's own `greenhouse.toml` sets `base = "main"` explicitly.
- Two plans run under one parent session share the WIP gate, which counts
  implement sessions by group, and the quota. A scheduler ignores stage
  sessions whose bead is not in its plan, so the two do not touch each
  other's runs.
- Still repo-specific: the plan must be exported under `.beads/plans/`, and
  the implement prompt runs `scripts/beads-export.sh`.
