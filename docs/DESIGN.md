# greenhouse — design

A software factory that fits within one person's Claude Max subscription.

greenhouse takes a Beads plan (epics, each with a "Done when"), runs every step
through spec → implement → verify → review, and ends each step at a draft PR a
human merges. The first product it builds is **itself**: the plan in
`.beads/plans/gr-boot.jsonl` is built by hand up to the bootstrap threshold
(gr-boot-6), and after that greenhouse runs its own remaining steps.

## Principles

1. **The control plane uses no LLM.** The scheduler is a deterministic Go loop.
   Only the stage sessions call Claude, each once, each bounded. Nothing thinks
   while idle. Orchestrators that burn tokens do so because their coordinator
   is itself an agent; this one is not. (ADR-0001)
2. **Quota is the clock.** On a Max subscription the limit is the rolling
   5-hour and 7-day windows, not dollars. The scheduler launches work only when
   headroom allows, and it never uses more than a configured share of the
   weekly window, so the rest stays free for the human's own work. (ADR-0002)
3. **Every stage is an agent-deck session.** You can attach to it, it is
   stamped on its PR, and it reports completion through agent-deck's sentinel
   and inbox. greenhouse adds no session machinery of its own. (ADR-0003)
4. **State lives in Beads and git, not in the scheduler.** A bead's stage is a
   label. Killing the scheduler at any moment loses nothing, and restarting it
   carries on from where the beads are.
5. **Intake is only approved beads, and a human merges.** No untrusted text (a
   stranger's issue or a PR comment) reaches an agent as instructions.
   Nothing auto-merges. (ADR-0004)
6. **Verification is deterministic.** Build, tests, and `nugit pr-render
   -fail-on fail`. An LLM's opinion of its own work is not a gate.
7. **Every rejection teaches.** A rejected PR becomes a nugit lesson
   candidate, so the next session's `context()` carries it.

## Pipeline

```
 plan (human + 1 interactive session)
   └─▶ Beads epics with "Done when", label  factory:approved
          │
 ┌──────── greenhouse tick (no LLM) ────────────────────────────────┐
 │ gates: quota headroom · WIP < cap · open agent PRs < cap       │
 │ pick next READY approved bead (bd ready: deps satisfied)       │
 │ launch stage session ─────────────▶ agent-deck launch         │
 │ ◀─ completion (sentinel → inbox) ──┘  (worktree, claude, nugit)│
 │ advance label · retry once · or park (stage:parked + reason)   │
 └────────────────────────────────────────────────────────────────┘
```

| stage | LLM? | input | output | on failure |
|---|---|---|---|---|
| `spec` | 1 short session | bead + `nugit context` | acceptance criteria + file plan appended to the bead | vague → `parked: needs-human` |
| `implement` | 1 session, turn-capped | bead + spec + context | commits on a worktree branch, draft PR | → `parked` |
| `verify` | **no** | the PR branch | pass/fail + log | 1 retry: log goes back to implement; then `parked` |
| `review` | **human** | draft PR | merge → bead closed | reject → `learn` |
| `learn` | 1 short session | rejection reason + diff | `.nugit/lessons/*.md` (proposed) | — |

The bead label records the stage (`stage:spec|implement|verify|review|parked`).
The scheduler's only memory is the labels, `agent-deck session children --json`
and `gh pr list`.

## Gates (checked every tick, all deterministic)

- **Quota:** `agent-deck usage --json` reads the Claude rate limits that
  Claude Code's statusLine pushes (`agent-deck usage ingest claude`). Launch
  only if 5-hour used < `quota.five_hour_max` and 7-day used <
  `quota.weekly_share`. If the reading is stale or missing, fall back to
  reactive mode: a session that hits "usage limit reached" parks the tick until
  the reported reset time.
- **WIP:** at most `wip.implement` implement sessions at once (default 1).
- **Review backlog:** at most `wip.open_prs` open agent PRs (default 3). A
  factory that out-produces its reviewer only adds to the queue.

## Session contract

The scheduler launches each stage roughly like this:

```
agent-deck launch <repo> -worktree <branch> -b -c claude -g greenhouse \
  -assert-done -message-file prompts/<stage>.md(rendered)
```

The rendered prompt carries the bead id, the stage, the "Done when", the
turn budget, and the rules: load `nugit context -budget 12000` for touched
paths first; commit with trailers; open a **draft** PR stamped with the
session; end with the `===AGENTDECK_DONE===` sentinel. Prompts are versioned
files in `prompts/`, so a change to how the factory works is a reviewed diff.

## Ledger

One JSONL line per stage run in `.greenhouse/ledger.jsonl` (gitignored), with
bead, stage, session id, start/end, quota before/after, retries and outcome.
`greenhouse report` rolls it up: quota used per merged PR, cycle time, park
rate, and rejection reasons. This is how we judge whether the factory is
working, rather than going by impressions.

## Where it runs

It runs on the laptop first (gr-boot-1…gr-boot-6). On the always-on build host it
runs under a dedicated unprivileged user (key-only SSH, not in the docker group)
in a capped systemd slice, with agent-deck remote-drained to the laptop
(gr-boot-9). Its first external customer comes next (gr-boot-10).

## Out of scope (for now)

- Slack, Linear or GitHub-issue intake. Beads are the only way in.
- Parallel implement workers beyond `wip.implement`. Raise the cap only once
  the ledger shows headroom.
- Auto-merge, ever.
