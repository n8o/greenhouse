package launch

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/n8o/greenhouse/internal/stage"
)

// Sentinel starts agent-deck's completion line, the last line of a finished
// session's final message.
const Sentinel = "===AGENTDECK_DONE==="

// EventPrefix starts the line, just before the sentinel, that reports a
// stage session's outcome as JSON:
//
//	GREENHOUSE-EVENT: {"kind":"implement-done","pr":12}
const EventPrefix = "GREENHOUSE-EVENT:"

// ErrNoResult is wrapped when a session's final message does not follow the
// result contract. The outcome is then unknown, and the caller must not guess.
var ErrNoResult = errors.New("no valid session result")

// Reported lists the event kinds a stage session may report, by stage.
var Reported = map[stage.Stage][]stage.EventKind{
	stage.Spec:      {stage.SpecDone, stage.SpecVague, stage.NeedsHuman},
	stage.Implement: {stage.ImplementDone, stage.ImplementFailed, stage.NeedsHuman},
	stage.Learn:     {stage.LearnDone, stage.NeedsHuman},
}

// wireEvent is the JSON of the event line. Each kind takes exactly the
// fields it needs.
type wireEvent struct {
	Kind     stage.EventKind `json:"kind"`
	PR       int             `json:"pr,omitempty"`
	Reason   string          `json:"reason,omitempty"`
	Question string          `json:"question,omitempty"`
}

// ParseResult reads the outcome of a stage-st session from its final message.
// The message must end with the event line followed by the sentinel line.
// Anything else (a missing, repeated or malformed line, a kind st cannot
// report, a missing or extra field) is an error wrapping ErrNoResult.
func ParseResult(st stage.Stage, content string) (stage.Event, error) {
	fail := func(format string, args ...any) (stage.Event, error) {
		return stage.Event{}, fmt.Errorf("%w: %s session: %s", ErrNoResult, st, fmt.Sprintf(format, args...))
	}
	kinds, ok := Reported[st]
	if !ok {
		return fail("stage runs no session")
	}
	var lines []string
	for l := range strings.Lines(content) {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) < 2 || !strings.HasPrefix(lines[len(lines)-1], Sentinel) {
		return fail("last line is not the %s sentinel", Sentinel)
	}
	sentinel, eventLine := lines[len(lines)-1], lines[len(lines)-2]
	if n := countContaining(lines, EventPrefix); n != 1 {
		return fail("want exactly one %s line, got %d", EventPrefix, n)
	}
	payload, ok := strings.CutPrefix(eventLine, EventPrefix)
	if !ok {
		return fail("the %s line must come right before the sentinel", EventPrefix)
	}

	dec := json.NewDecoder(strings.NewReader(payload))
	dec.DisallowUnknownFields()
	var w wireEvent
	if err := dec.Decode(&w); err != nil {
		return fail("event JSON: %v", err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return fail("trailing text after the event JSON")
	}
	if !slices.Contains(kinds, w.Kind) {
		return fail("kind %q is not one of %v", w.Kind, kinds)
	}

	ev := stage.Event{Kind: w.Kind}
	switch w.Kind {
	case stage.NeedsHuman:
		if w.Question == "" || w.PR != 0 || w.Reason != "" {
			return fail("%s takes exactly a question", w.Kind)
		}
		ev.Reason = w.Question
	case stage.ImplementDone:
		if w.PR <= 0 || w.Reason != "" || w.Question != "" {
			return fail("%s takes exactly a pr number > 0", w.Kind)
		}
		ev.PR = w.PR
	case stage.SpecVague, stage.ImplementFailed:
		if w.Reason == "" || w.PR != 0 || w.Question != "" {
			return fail("%s takes exactly a reason", w.Kind)
		}
		ev.Reason = w.Reason
	default: // spec-done, learn-done
		if w.PR != 0 || w.Reason != "" || w.Question != "" {
			return fail("%s takes no fields", w.Kind)
		}
	}
	if strings.Contains(sentinel, "status=fail") && (w.Kind == stage.SpecDone || w.Kind == stage.ImplementDone || w.Kind == stage.LearnDone) {
		return fail("sentinel says status=fail but the event is %s", w.Kind)
	}
	if _, _, err := stage.Next(st, ev); err != nil {
		return fail("%v", err)
	}
	return ev, nil
}

// countContaining counts the lines that mention s anywhere, so an event line
// quoted or wrapped in markup counts too and makes the result ambiguous.
func countContaining(lines []string, s string) int {
	n := 0
	for _, l := range lines {
		if strings.Contains(l, s) {
			n++
		}
	}
	return n
}
