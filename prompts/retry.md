# greenhouse stage: {{.Stage}} — {{.Bead}} (retry)

You are still the **{{.Stage}}** stage for bead **{{.Bead}}**: {{.Title}}.
greenhouse has sent you back to work in this same session, on branch
`{{.Branch}}` in your own worktree. Everything you did before is still here;
build on it rather than starting over.

## Why you are running again
{{.Reason}}

## Done when (from the bead, as it is now)
{{.DoneWhen}}
{{- if .Spec}}

## Spec
{{.Spec}}
{{- end}}

## Steps
{{- if eq .Stage "implement"}}
1. Fix what the section above reports before anything else. Load
   `nugit context` for every path you touch.
2. Run the repo's checks (AGENTS.md "Build / test") until they pass.
3. Commit the fix on this branch (with trailers when it carries a decision)
   and push it. The draft PR from your previous attempt picks it up: do
   **not** open a new PR, and do not run `gh pr ready`, merge, or approve.
4. Report `implement-done` with the **same** PR number as before. If you
   cannot get the PR to pass, report `implement-failed` with why.
{{- else}}
1. Re-read the bead: a human may have changed it since your last turn.
2. Redo or finish this stage from where your work stands now, following
   the instructions of your first message.
{{- end}}

Your previous final message does not count for this run: greenhouse reads
only a reply written after this message. End this turn with a fresh result.

{{template "rules" .}}
{{template "contract" .}}
