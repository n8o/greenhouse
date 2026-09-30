package launch

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/n8o/greenhouse/internal/stage"
)

const done = Sentinel + " status=ok summary=did it"

func TestParseResult(t *testing.T) {
	tests := []struct {
		name    string
		st      stage.Stage
		content string
		want    stage.Event
	}{
		{"spec done", stage.Spec, "Recorded the spec.\n" + EventPrefix + ` {"kind":"spec-done"}` + "\n" + done,
			stage.Event{Kind: stage.SpecDone}},
		{"spec vague", stage.Spec, EventPrefix + ` {"kind":"spec-vague","reason":"no Done when"}` + "\n" + Sentinel + " status=fail summary=vague",
			stage.Event{Kind: stage.SpecVague, Reason: "no Done when"}},
		{"implement done carries the PR", stage.Implement, "  " + EventPrefix + `{"kind":"implement-done","pr":12}  ` + "\n\n" + done + "\n",
			stage.Event{Kind: stage.ImplementDone, PR: 12}},
		{"implement failed", stage.Implement, EventPrefix + ` {"kind":"implement-failed","reason":"tests red"}` + "\n" + done,
			stage.Event{Kind: stage.ImplementFailed, Reason: "tests red"}},
		{"needs-human carries the question", stage.Implement, EventPrefix + ` {"kind":"needs-human","question":"A or B? I recommend A"}` + "\n" + done,
			stage.Event{Kind: stage.NeedsHuman, Reason: "A or B? I recommend A"}},
		{"learn done", stage.Learn, EventPrefix + ` {"kind":"learn-done"}` + "\n" + done,
			stage.Event{Kind: stage.LearnDone}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseResult(tt.st, tt.content)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("ParseResult = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseResultRejects(t *testing.T) {
	ev := func(js string) string { return EventPrefix + " " + js + "\n" + done }
	tests := []struct {
		name    string
		st      stage.Stage
		content string
		want    string
	}{
		{"empty", stage.Spec, "", "sentinel"},
		{"no sentinel", stage.Spec, EventPrefix + ` {"kind":"spec-done"}`, "sentinel"},
		{"sentinel not last", stage.Spec, EventPrefix + ` {"kind":"spec-done"}` + "\n" + done + "\nP.S.", "sentinel"},
		{"no event line", stage.Spec, "All done.\n" + done, "exactly one"},
		{"two event lines", stage.Spec, EventPrefix + ` {"kind":"spec-done"}` + "\n" + ev(`{"kind":"spec-done"}`), "exactly one"},
		{"event not before sentinel", stage.Spec, EventPrefix + ` {"kind":"spec-done"}` + "\nbye\n" + done, "right before"},
		{"fenced event", stage.Spec, "`" + EventPrefix + ` {"kind":"spec-done"}` + "`\n" + done, "right before"},
		{"malformed json", stage.Spec, ev(`{"kind":"spec-done"`), "event JSON"},
		{"trailing text", stage.Spec, ev(`{"kind":"spec-done"} ok`), "trailing"},
		{"unknown field", stage.Spec, ev(`{"kind":"spec-done","pr_url":"x"}`), "unknown field"},
		{"unknown kind", stage.Spec, ev(`{"kind":"finished"}`), "not one of"},
		{"kind of another stage", stage.Spec, ev(`{"kind":"implement-done","pr":3}`), "not one of"},
		{"verify is not a session", stage.Verify, ev(`{"kind":"verify-pass","pr":3}`), "runs no session"},
		{"implement-done without pr", stage.Implement, ev(`{"kind":"implement-done"}`), "pr number"},
		{"implement-done with pr 0", stage.Implement, ev(`{"kind":"implement-done","pr":0}`), "pr number"},
		{"implement-done negative pr", stage.Implement, ev(`{"kind":"implement-done","pr":-4}`), "pr number"},
		{"implement-done string pr", stage.Implement, ev(`{"kind":"implement-done","pr":"12"}`), "event JSON"},
		{"needs-human without question", stage.Spec, ev(`{"kind":"needs-human"}`), "question"},
		{"needs-human with reason not question", stage.Spec, ev(`{"kind":"needs-human","reason":"hm"}`), "question"},
		{"spec-vague without reason", stage.Spec, ev(`{"kind":"spec-vague"}`), "reason"},
		{"implement-failed without reason", stage.Implement, ev(`{"kind":"implement-failed"}`), "reason"},
		{"spec-done with extra field", stage.Spec, ev(`{"kind":"spec-done","reason":"x"}`), "no fields"},
		{"done but sentinel failed", stage.Spec, EventPrefix + ` {"kind":"spec-done"}` + "\n" + Sentinel + " status=fail summary=x", "status=fail"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseResult(tt.st, tt.content)
			if !errors.Is(err, ErrNoResult) {
				t.Fatalf("ParseResult = %+v, %v; want an error wrapping ErrNoResult", got, err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not mention %q", err, tt.want)
			}
		})
	}
}

// Every example the prompt offers a stage must parse for that stage, so the
// prompt and the parser cannot drift apart.
func TestEventDocsParse(t *testing.T) {
	fill := strings.NewReplacer(`<number>`, `7`)
	for st, kinds := range Reported {
		for _, k := range kinds {
			doc, ok := eventDocs[k]
			if !ok {
				t.Fatalf("no EventDoc for %s", k)
			}
			var probe map[string]any
			js := fill.Replace(doc.JSON)
			if err := json.Unmarshal([]byte(js), &probe); err != nil {
				t.Fatalf("%s example %s: %v", k, doc.JSON, err)
			}
			got, err := ParseResult(st, EventPrefix+" "+js+"\n"+done)
			if err != nil {
				t.Errorf("%s: the %s example does not parse: %v", st, k, err)
			} else if got.Kind != k {
				t.Errorf("%s: example parsed as %s, want %s", st, got.Kind, k)
			}
		}
	}
}
