package launch

import (
	"bytes"
	"fmt"
	"io/fs"
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
	// PR is the rejected pull request learn studies (required there), and
	// HeadRef its branch, which may be gone after the close.
	PR      int
	HeadRef string
	Stage   stage.Stage
	// Branch is the worktree branch; "" means DefaultBranch.
	Branch string
	// ExistingBranch launches on Branch as it is instead of creating it: an
	// implement retry continues the branch of its open PR.
	ExistingBranch bool
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

// Render renders <stage>.md from prompts, with contract.md, for req. A
// template that uses a missing field is an error.
func Render(prompts fs.FS, req Request, maxTurns int) (string, error) {
	if req.Stage == stage.Learn && req.Reason == "" {
		return "", fmt.Errorf("render: learn needs the rejection reason")
	}
	if req.Stage == stage.Learn && req.PR <= 0 {
		return "", fmt.Errorf("render: learn needs the rejected PR")
	}
	return render(prompts, string(req.Stage)+".md", req, maxTurns)
}

// RetryPrompt is the template, next to the stage prompts, that a resumed
// stage session receives instead of its stage prompt.
const RetryPrompt = "retry.md"

// RenderRetry renders retry.md from prompts for req, the follow-up message a
// stage session that already ran gets when its stage runs again. req.Reason
// says why it runs again and is required.
func RenderRetry(prompts fs.FS, req Request, maxTurns int) (string, error) {
	if req.Reason == "" {
		return "", fmt.Errorf("render retry: the reason for the retry is required")
	}
	return render(prompts, RetryPrompt, req, maxTurns)
}

func render(prompts fs.FS, name string, req Request, maxTurns int) (string, error) {
	kinds, ok := Reported[req.Stage]
	if !ok {
		return "", fmt.Errorf("render: stage %q runs no session", req.Stage)
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

	tmpl, err := template.New(name).Option("missingkey=error").
		ParseFS(prompts, name, "contract.md")
	if err != nil {
		return "", fmt.Errorf("render: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("render %s: %w", name, err)
	}
	return buf.String(), nil
}
