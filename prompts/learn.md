# greenhouse stage: learn — {{.Bead}}

You are the **learn** stage for bead **{{.Bead}}**: {{.Title}}.
A human rejected its PR. Turn the rejection into a lesson so the next session
does not repeat the mistake.

You work on branch `{{.Branch}}` in your own worktree.

## Done when (from the bead)
{{.DoneWhen}}

## The rejected PR
PR **#{{.PR}}**, branch {{if .HeadRef}}`{{.HeadRef}}`{{else}}unknown{{end}}, closed unmerged.
The branch may be deleted after the close; read the PR, not the branch.

## Why the PR was rejected
{{.Reason}}

## Steps
1. Read what was rejected and why:
   - `gh pr diff {{.PR}}` for the diff,
   - `gh pr view {{.PR}} --comments` for the human's review and comments,
   - `gh pr view {{.PR}} --json headRefOid -q .headRefOid` for the head commit.
2. Load `nugit context -path <file> -task "<task>" -budget {{.ContextBudget}}`
   for the paths the diff touched, and find their components in
   `.nugit/architecture/workspace.dsl`.
3. Write **one** new lesson file, `.nugit/lessons/<slug>.md`, where `<slug>`
   is a short kebab-case name for the insight. Use exactly this shape:

   ```markdown
   ---
   schema_version: 1
   id: LESSON-<slug>
   type: lesson
   scope: <the touched component, as named in workspace.dsl>
   applies_to_paths:
     - <each path glob the rejected diff touched>
   status: proposed
   created: <now, RFC 3339 UTC, e.g. 2026-10-16T09:00:00Z>
   provenance:
     commit: <the rejected PR's headRefOid>
   confidence: low
   ---

   # Lesson — <the insight in one line>

   **Trigger:** <the situation that led to the mistake, as the next session will meet it>

   **Insight:** <the rule that avoids it, and the evidence from PR #{{.PR}}>

   **Keywords:** <comma-separated terms a later search would use>
   ```

   `scope` is one component; when the diff spans several, scope the lesson
   to the one the mistake belongs to and list every touched path under
   `applies_to_paths`.
4. Change nothing outside `.nugit/lessons/`: no code, no ADRs, no `.beads/`.
   Leave `status: proposed`. Never set `status: active` and never run
   `nugit ratify`: the human ratifies the lesson by merging its PR and
   running `nugit ratify`.
5. Commit it with trailers (`learned:`, `affects:`, `keywords:`), push
   `{{.Branch}}` and open it as its own **draft** PR:
   `gh pr create --draft --title "lesson: <slug> (from #{{.PR}})"`. Do not
   add the greenhouse label. End the body with
   `Agent-Deck-Session: <title> (<$AGENTDECK_INSTANCE_ID>)` and
   `Agent-Deck-Host: <hostname>`.
6. Do **not** run `gh pr ready`, merge, or approve. greenhouse checks the PR
   (open, draft, only new `status: proposed` lessons under `.nugit/lessons/`)
   before it accepts `learn-done`; anything else parks the bead.

{{template "rules" .}}
{{template "contract" .}}
