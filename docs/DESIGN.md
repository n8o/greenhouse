# greenhouse — design

A software factory that fits within one person's Claude Max subscription.

greenhouse takes a Beads plan (epics, each with a "Done when"), runs every step
through spec → implement → verify → review, and ends each step at a PR that is
marked ready for review only after it is validated. A human merges. The first product it builds is **itself**: the plan in
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
4. **Git carries the plan, not the run.** A bead's stage is a label in the
   *local* bd database, never exported. Git changes only when the plan or a
   step's completion changes, and a step closes inside its own PR, which may
   also carry that step's spec (acceptance criteria, design, notes). Killing the
   scheduler loses nothing: run state can be rebuilt from bd, agent-deck, `gh`
   and the ledger. (ADR-0005, ADR-0008)
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
| `implement` | 1 session, turn-capped | bead + spec + context | commits on a worktree branch, PR opened as **draft** | → `parked` |
| `verify` | **no** | the PR branch | pass → `gh pr ready`; fail → log | 1 retry: log goes back to implement; then `parked` |
| `review` | **human** | ready PR | merge → bead closed | reject → `learn` |
| `learn` | 1 short session | rejected PR (diff, review/close comment) | one `.nugit/lessons/*.md`, `status: proposed`, on its own **draft** PR | PR fails the lesson check → `parked` |

A rejection is the human closing the PR unmerged; the latest review or PR
comment (`gh pr close -c "why"`) is its reason, kept in the ledger. Before
it accepts `learn-done`, the scheduler checks the learn PR with no LLM
(`internal/learncheck`): open, draft, changing only `.nugit/lessons/`, and
every new lesson `status: proposed`. The lesson PR carries no greenhouse
label, so it never holds back implement launches. Either way the bead parks;
the human ratifies the lesson by merging it and running `nugit ratify`.

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
paths first; commit with trailers; open the PR as a **draft**, stamped with the
session; end with the `===AGENTDECK_DONE===` sentinel. Prompts are versioned
files in `prompts/`, so a change to how the factory works is a reviewed diff.

A stage that runs again for the same bead (a verify-fail retry, a relaunch
after a usage-limit park, a spec after an unpark) **resumes its existing
session** instead of launching one, since its title is taken: greenhouse
starts it again if it is stopped and sends it `prompts/retry.md` with why it
runs again. Only a reply written after that message counts as the run's
result; the previous attempt's completion is ignored. If a human removed the
session, a new one is launched on the existing branch. (ADR-0009)

## Exceptions: the conductor (ADR-0006)

The scheduler never guesses. Anything it can't route becomes `stage:parked`
with a reason. An agent-deck **conductor** watches for parks and the ledger
and notifies the human with the question and a drafted answer. It never moves
beads, merges or launches stages. Routine work costs no tokens between
sessions, and the conductor spends tokens only on surprises. The scheduler
polls session state itself and needs no notify-daemon.

## Ledger

One JSONL line per stage run in `.greenhouse/ledger.jsonl` (gitignored), with
bead, stage, session id, start/end, quota before/after, retries and outcome.
`greenhouse report` rolls it up: quota used per merged PR, cycle time, park
rate, retries, and rejection reasons. This is how we judge whether the factory
is working, rather than going by impressions.

The report also asks whether scoped memory earns its keep. It joins each LLM
stage run to nugit's local usage log (`.nugit/.cache/usage.jsonl`), by the
agent-deck session id when the log carries it and otherwise by the run's
branch and time window. For each run it reports context calls, the calls made
before the run's first commit, empty bundles, and unresolved paths. It then
splits beads into those that loaded context and those that did not, and
compares their verify retries, park rate, merges and rejections, and review
rounds. Below a minimum sample (`-min-sample`, default 5 beads per group) it
prints only the counts and says there are too few runs to compare. It draws
no conclusion from a handful of runs.

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
