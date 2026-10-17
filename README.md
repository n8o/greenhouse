# greenhouse

A software factory that fits within one person's Claude Max subscription.
A deterministic scheduler moves approved Beads plan steps through spec →
implement → verify → review, runs each stage as an agent-deck Claude Code
session, gates every launch on remaining quota, and stops at a PR that is
marked ready only once it is validated. A human merges.

## Use it on your repo

You need Claude Code (logged in to a Max plan), agent-deck, Beads (`bd`),
nugit, `gh` (authenticated) and `jq` on the host.

```sh
go install github.com/n8o/greenhouse/cmd/greenhouse@latest
go install github.com/n8o/nugit/cmd/nugit@latest

# A config can live anywhere; this one runs plan "app-v2" on ~/src/app.
greenhouse init -repo ~/src/app -plan app-v2 -config ~/greenhouse/app.toml
$EDITOR ~/greenhouse/app.toml        # add the repo's build/test commands to verify.commands
greenhouse doctor -config ~/greenhouse/app.toml
```

The repo needs its plan exported to `.beads/plans/` and a
`scripts/beads-export.sh` (copy [greenhouse's](scripts/beads-export.sh)).
Then, from inside an agent-deck session (it becomes the parent of every
stage session):

```sh
bd label add app-v2-1 factory:approved                # release a step to the factory
greenhouse tick -dry-run -config ~/greenhouse/app.toml # what it would do
greenhouse run -config ~/greenhouse/app.toml           # tick every 5 minutes
```

The base branch is origin's default unless the config sets `base`, the stage
prompts are built in (a `prompts` directory overrides them), and run state
stays in the repo's `.greenhouse/`, excluded from git through the clone's
`info/exclude`. See [docs/DESIGN.md](docs/DESIGN.md#any-repo-adr-0011).

## More

- Design: [docs/DESIGN.md](docs/DESIGN.md)
- Decisions: [.nugit/decisions/](.nugit/decisions/)
- Plan (it builds itself): `.beads/plans/gr-boot.jsonl`

When the human closes a PR unmerged, the review or close comment is
recorded as the reason and a learn session proposes a nugit lesson
(`status: proposed`) on its own draft PR. greenhouse checks that PR
deterministically before it accepts it; the human ratifies the lesson.

`greenhouse report [--json]` rolls the local ledger up: quota per merged PR,
cycle time, park rate, retries, rejections, and whether stage sessions load
scoped memory (`nugit context`) and whether that helps. It compares outcomes
only once each group has `-min-sample` beads (default 5); below that it
prints the counts and says there are too few runs to compare.

Status: pre-alpha, bootstrapping. Built by hand up to gr-boot-6, then by itself.
