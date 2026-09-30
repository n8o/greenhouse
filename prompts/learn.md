# greenhouse stage: learn — {{.Bead}}

You are the **learn** stage for bead **{{.Bead}}**: {{.Title}}.
A human rejected its PR. Turn the rejection into a lesson so the next session
does not repeat the mistake.

## Done when (from the bead)
{{.DoneWhen}}

## Why the PR was rejected
{{.Reason}}

## Steps
1. Read the rejected diff and load `nugit context` for the paths it touched.
2. Write one lesson under `.nugit/lessons/` with `status: proposed`, scoped to
   the touched components. State the mistake, the rule that avoids it, and
   the evidence.
3. Commit it with trailers and open it as its own **draft** PR
   (`gh pr create --draft`), stamped with `Agent-Deck-Session:` and
   `Agent-Deck-Host:` lines. The human ratifies it; never merge or approve.
4. Do not touch `.beads/`.

{{template "rules" .}}
{{template "contract" .}}
