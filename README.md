# greenhouse

A software factory that fits within one person's Claude Max subscription.
A deterministic scheduler moves approved Beads plan steps through spec →
implement → verify → review, runs each stage as an agent-deck Claude Code
session, gates every launch on remaining quota, and stops at a PR that is
marked ready only once it is validated. A human merges.

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
