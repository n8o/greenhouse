{{define "rules"}}## Rules
- This is an unattended greenhouse stage session. Nobody is watching; do not
  ask questions in chat. If only a human can decide, report `needs-human`
  (below) with the question and your recommendation, and stop.
- You have a budget of {{.MaxTurns}} turns. If the work does not fit, stop
  and report instead of running on.
- Before a non-trivial edit, load context for the path:
  `nugit context -path <file> -task "<task>" -budget {{.ContextBudget}}`.
- Read the repo's agent instructions (AGENTS.md, CLAUDE.md, whichever it
  has) and the ADRs in `.nugit/decisions/`. Their rejected alternatives are
  settled; do not re-propose them.
- This session's objective is bead {{.Bead}}, and nothing else. First run
  `nugit objective claim {{.Bead}}`, so the commit-msg hook names it on every
  commit and the PR view opens with it. If that command fails, end every
  commit message with the line `objective: {{.Bead}}` yourself. Work you find
  that belongs to another bead goes in your final report, not in this branch.
{{end}}

{{define "contract"}}## Result contract
End your final message with exactly these two lines, and nothing after them:

    {{.EventPrefix}} <one-line JSON event>
    {{.Sentinel}} status=<ok|fail> summary=<one line>

The event is the machine-readable outcome greenhouse acts on. Write it once,
on its own line, as plain text (no code fence, no quotes), and use exactly one
of these shapes:
{{range .Events}}
- `{{.JSON}}` — {{.When}}
{{- end}}

A missing or malformed event line is treated as a failed session.
{{end}}
