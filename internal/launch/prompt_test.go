package launch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/n8o/greenhouse/internal/stage"
)

// promptDir is the committed prompts/ directory.
var promptDir = filepath.Join("..", "..", "prompts")

func req(st stage.Stage) Request {
	return Request{Bead: "x-1", Title: "Do the thing", DoneWhen: "Done when: it works.",
		Spec: "- [ ] go test passes", Reason: "because", Stage: st}
}

func TestRenderCommittedPrompts(t *testing.T) {
	common := []string{
		"x-1", "Do the thing", "Done when: it works.",
		"42 turns",            // turn budget
		"-budget 12000",       // nugit context
		EventPrefix, Sentinel, // result contract
		`{"kind":"needs-human","question":`,
	}
	perStage := map[stage.Stage][]string{
		stage.Spec: {`{"kind":"spec-done"}`, `"kind":"spec-vague","reason"`, "bd update x-1"},
		stage.Implement: {
			"feature/x-1-implement", "go test passes", // branch, spec
			`"kind":"implement-done","pr":<number>`, `"kind":"implement-failed","reason"`,
			"gh pr create --draft", "Do **not** run `gh pr ready`", // handoff: verify marks ready
			`bd close x-1 --reason "this PR"`, "**only the x-1 line**", // ADR-0005
			"decision:", "Agent-Deck-Session:",
		},
		stage.Learn: {`{"kind":"learn-done"}`, ".nugit/lessons/", "status: proposed", "because"},
	}
	for st, want := range perStage {
		t.Run(string(st), func(t *testing.T) {
			got, err := Render(promptDir, req(st), 42)
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range append(common, want...) {
				if !strings.Contains(got, w) {
					t.Errorf("%s prompt lacks %q", st, w)
				}
			}
			if strings.Contains(got, "<no value>") {
				t.Errorf("%s prompt renders a missing value", st)
			}
			// A stage is only offered the events it may report.
			if st != stage.Implement && strings.Contains(got, `"implement-done"`) {
				t.Errorf("%s prompt offers implement-done", st)
			}
		})
	}
}

func TestRenderRejects(t *testing.T) {
	if _, err := Render(promptDir, req(stage.Verify), 1); err == nil {
		t.Error("verify runs no session; Render must fail")
	}
	r := req(stage.Learn)
	r.Reason = ""
	if _, err := Render(promptDir, r, 1); err == nil {
		t.Error("learn without a rejection reason must fail")
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "contract.md"), []byte(`{{define "contract"}}{{end}}`), 0o644)
	os.WriteFile(filepath.Join(dir, "spec.md"), []byte("{{.Nope}}"), 0o644)
	if _, err := Render(dir, req(stage.Spec), 1); err == nil {
		t.Error("a template using an unknown field must fail")
	}
	if _, err := Render(t.TempDir(), req(stage.Spec), 1); err == nil {
		t.Error("a missing template must fail")
	}
}
