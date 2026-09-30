package launch

import (
	"bytes"
	"fmt"
	"path/filepath"
	"text/template"

	"github.com/n8o/greenhouse/internal/stage"
)

// ContextBudget is the token budget every stage session passes to
// `nugit context` before it edits a path.
const ContextBudget = 12000

// Request is one stage run of one bead.
type Request struct {
	Bead     string // bead id, e.g. "gr-boot-3"
	Title    string
	DoneWhen string
	Spec     string // implement: the spec stage's output
	// Reason is context from the scheduler: the verify log on an implement
	// retry, or the rejection reason for learn (required there).
	Reason string
	Stage  stage.Stage
	// Branch is the worktree branch; "" means DefaultBranch.
	Branch string
}

// DefaultBranch is the worktree branch of a stage run that names none.
// agent-deck prefixes a branch that lacks "feature/", so it has one.
func DefaultBranch(bead string, st stage.Stage) string {
	return fmt.Sprintf("feature/%s-%s", bead, st)
}

// EventDoc is one event shape the prompt offers the session.
type EventDoc struct {
	JSON string // example event line payload
	When string
}

// PromptData is what a prompts/<stage>.md template sees.
type PromptData struct {
	Request
	MaxTurns      int
	ContextBudget int
	EventPrefix   string
	Sentinel      string
	Events        []EventDoc
}

// eventDocs documents, per event kind, the example payload and when to use
// it. ParseResult enforces the same shapes.
var eventDocs = map[stage.EventKind]EventDoc{
	stage.SpecDone:        {`{"kind":"spec-done"}`, "the acceptance criteria and file plan are recorded on the bead"},
	stage.SpecVague:       {`{"kind":"spec-vague","reason":"<what is missing>"}`, "the bead is too vague to spec"},
	stage.ImplementDone:   {`{"kind":"implement-done","pr":<number>}`, "the draft PR is open; pr is its number"},
	stage.ImplementFailed: {`{"kind":"implement-failed","reason":"<why>"}`, "you could not get to a draft PR"},
	stage.LearnDone:       {`{"kind":"learn-done"}`, "the lesson's draft PR is open"},
	stage.NeedsHuman:      {`{"kind":"needs-human","question":"<question + your recommendation>"}`, "only a human can decide how to go on"},
}

// Render renders prompts/<stage>.md from dir, with prompts/contract.md, for
// req. A template that uses a missing field is an error.
func Render(dir string, req Request, maxTurns int) (string, error) {
	kinds, ok := Reported[req.Stage]
	if !ok {
		return "", fmt.Errorf("render: stage %q runs no session", req.Stage)
	}
	if req.Stage == stage.Learn && req.Reason == "" {
		return "", fmt.Errorf("render: learn needs the rejection reason")
	}
	if req.Branch == "" {
		req.Branch = DefaultBranch(req.Bead, req.Stage)
	}
	data := PromptData{
		Request:       req,
		MaxTurns:      maxTurns,
		ContextBudget: ContextBudget,
		EventPrefix:   EventPrefix,
		Sentinel:      Sentinel,
	}
	for _, k := range kinds {
		data.Events = append(data.Events, eventDocs[k])
	}

	name := string(req.Stage) + ".md"
	tmpl, err := template.New(name).Option("missingkey=error").
		ParseFiles(filepath.Join(dir, name), filepath.Join(dir, "contract.md"))
	if err != nil {
		return "", fmt.Errorf("render: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("render %s: %w", name, err)
	}
	return buf.String(), nil
}
