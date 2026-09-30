package stage

import (
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

type pair struct {
	s Stage
	k EventKind
}

// valid is every (stage, event) pair with a transition. Any pair missing
// here must be rejected.
var valid = map[pair]bool{
	{Spec, SpecDone}:             true,
	{Spec, SpecVague}:            true,
	{Spec, NeedsHuman}:           true,
	{Implement, ImplementDone}:   true,
	{Implement, ImplementFailed}: true,
	{Implement, NeedsHuman}:      true,
	{Verify, VerifyPass}:         true,
	{Verify, VerifyFail}:         true,
	{Review, Merged}:             true,
	{Review, Rejected}:           true,
	{Learn, LearnDone}:           true,
	{Learn, NeedsHuman}:          true,
	{Parked, Unparked}:           true,
}

func TestNextValid(t *testing.T) {
	tests := []struct {
		name string
		s    Stage
		e    Event
		want Stage
		acts []Action
	}{
		{"spec done", Spec, Event{Kind: SpecDone}, Implement,
			[]Action{{Kind: ActLaunch, Stage: Implement}}},
		{"spec vague", Spec, Event{Kind: SpecVague, Reason: "which API?"}, Parked,
			[]Action{{Kind: ActPark, Reason: "which API?"}}},
		{"spec needs human", Spec, Event{Kind: NeedsHuman, Reason: "q"}, Parked,
			[]Action{{Kind: ActPark, Reason: "q"}}},
		{"implement done", Implement, Event{Kind: ImplementDone, PR: 7}, Verify,
			[]Action{{Kind: ActVerify, PR: 7}}},
		{"implement failed", Implement, Event{Kind: ImplementFailed, Reason: "turn cap"}, Parked,
			[]Action{{Kind: ActPark, Reason: "turn cap"}}},
		{"implement needs human", Implement, Event{Kind: NeedsHuman, Reason: "q"}, Parked,
			[]Action{{Kind: ActPark, Reason: "q"}}},
		{"verify pass marks ready", Verify, Event{Kind: VerifyPass, PR: 7}, Review,
			[]Action{{Kind: ActMarkReady, PR: 7}}},
		{"verify fail retries", Verify, Event{Kind: VerifyFail, Reason: "log", Retries: 0}, Implement,
			[]Action{{Kind: ActRecord, Reason: "log"}, {Kind: ActLaunch, Stage: Implement, Reason: "log"}}},
		{"verify fail after retry parks", Verify, Event{Kind: VerifyFail, Reason: "log", Retries: 1}, Parked,
			[]Action{{Kind: ActRecord, Reason: "log"}, {Kind: ActPark, Reason: "verify failed with 1/1 retries used: log"}}},
		{"verify fail beyond limit parks", Verify, Event{Kind: VerifyFail, Reason: "log", Retries: 5}, Parked,
			[]Action{{Kind: ActRecord, Reason: "log"}, {Kind: ActPark, Reason: "verify failed with 5/1 retries used: log"}}},
		{"merged", Review, Event{Kind: Merged}, Done,
			[]Action{{Kind: ActCloseBead}}},
		{"rejected learns", Review, Event{Kind: Rejected, Reason: "wrong layer"}, Learn,
			[]Action{{Kind: ActRecord, Reason: "wrong layer"}, {Kind: ActLaunch, Stage: Learn, Reason: "wrong layer"}}},
		{"learn done parks", Learn, Event{Kind: LearnDone}, Parked,
			[]Action{{Kind: ActPark, Reason: "PR rejected and lesson proposed: re-plan and unpark, or close the bead"}}},
		{"learn needs human", Learn, Event{Kind: NeedsHuman, Reason: "q"}, Parked,
			[]Action{{Kind: ActPark, Reason: "q"}}},
		{"unparked respecs", Parked, Event{Kind: Unparked}, Spec,
			[]Action{{Kind: ActLaunch, Stage: Spec}}},
	}
	covered := map[pair]bool{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, acts, err := Next(tt.s, tt.e)
			if err != nil {
				t.Fatalf("Next(%s, %s): %v", tt.s, tt.e.Kind, err)
			}
			if got != tt.want {
				t.Errorf("stage = %s, want %s", got, tt.want)
			}
			if !reflect.DeepEqual(acts, tt.acts) {
				t.Errorf("actions = %+v, want %+v", acts, tt.acts)
			}
		})
		covered[pair{tt.s, tt.e.Kind}] = true
	}
	for p := range valid {
		if !covered[p] {
			t.Errorf("valid transition %s on %s has no test case", p.s, p.k)
		}
	}
}

// wellFormed returns an event of kind k carrying every field any transition
// needs, so a rejection can only come from the (stage, event) pair.
func wellFormed(k EventKind) Event {
	return Event{Kind: k, Reason: "r", PR: 1}
}

func TestNextEveryPair(t *testing.T) {
	for _, s := range All() {
		for _, k := range EventKinds() {
			t.Run(string(s)+"/"+string(k), func(t *testing.T) {
				got, acts, err := Next(s, wellFormed(k))
				if valid[pair{s, k}] {
					if err != nil {
						t.Fatalf("want a transition, got %v", err)
					}
					if !got.Valid() || len(acts) == 0 {
						t.Errorf("got stage %q with %d actions", got, len(acts))
					}
					return
				}
				if !errors.Is(err, ErrInvalid) {
					t.Fatalf("err = %v, want ErrInvalid", err)
				}
				if got != s || acts != nil {
					t.Errorf("invalid transition moved to %s with %+v", got, acts)
				}
			})
		}
	}
}

func TestNextMalformed(t *testing.T) {
	tests := []struct {
		name string
		s    Stage
		e    Event
		msg  string
	}{
		{"unknown stage", "shipping", Event{Kind: SpecDone}, `unknown stage "shipping"`},
		{"empty stage", "", Event{Kind: SpecDone}, `unknown stage ""`},
		{"unknown event", Spec, Event{Kind: "tea-break"}, `unknown event "tea-break"`},
		{"done is terminal", Done, Event{Kind: Unparked}, "no such transition"},
		{"vague without reason", Spec, Event{Kind: SpecVague}, "reason is required"},
		{"needs human without question", Implement, Event{Kind: NeedsHuman}, "reason is required"},
		{"failed without reason", Implement, Event{Kind: ImplementFailed}, "reason is required"},
		{"implement done without PR", Implement, Event{Kind: ImplementDone}, "PR number is required"},
		{"verify pass without PR", Verify, Event{Kind: VerifyPass, PR: -1}, "PR number is required"},
		{"verify fail without log", Verify, Event{Kind: VerifyFail}, "verify log"},
		{"negative retries", Verify, Event{Kind: VerifyFail, Reason: "log", Retries: -1}, "retries must be >= 0"},
		{"rejected without reason", Review, Event{Kind: Rejected}, "why the PR was rejected"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, acts, err := Next(tt.s, tt.e)
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tt.msg) {
				t.Fatalf("err = %v, want ErrInvalid containing %q", err, tt.msg)
			}
			if got != tt.s || acts != nil {
				t.Errorf("malformed event moved to %s with %+v", got, acts)
			}
		})
	}
}

// TestRetryOnceThenPark walks the ADR-0004 path end to end: a verify failure
// goes back to implement once, and the second one parks the bead.
func TestRetryOnceThenPark(t *testing.T) {
	s, _ := Start()
	steps := []Event{
		{Kind: SpecDone},
		{Kind: ImplementDone, PR: 3},
		{Kind: VerifyFail, Reason: "test failed", Retries: 0},
		{Kind: ImplementDone, PR: 3},
		{Kind: VerifyFail, Reason: "test failed", Retries: 1},
	}
	want := []Stage{Implement, Verify, Implement, Verify, Parked}
	for i, e := range steps {
		next, _, err := Next(s, e)
		if err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		if next != want[i] {
			t.Fatalf("step %d: %s on %s → %s, want %s", i, s, e.Kind, next, want[i])
		}
		s = next
	}
}

func TestStart(t *testing.T) {
	s, acts := Start()
	if s != Spec || !reflect.DeepEqual(acts, []Action{{Kind: ActLaunch, Stage: Spec}}) {
		t.Errorf("Start() = %s, %+v", s, acts)
	}
}

func TestAllValid(t *testing.T) {
	if len(All()) != 7 {
		t.Fatalf("All() has %d stages, want 7", len(All()))
	}
	for _, s := range All() {
		if !s.Valid() {
			t.Errorf("%s is not Valid", s)
		}
	}
	if Stage("stage:spec").Valid() {
		t.Error("a label is not a stage")
	}
}

// TestPure guards ADR-0001: the package imports nothing that can do I/O,
// read a clock or draw random numbers, and no other greenhouse package.
func TestPure(t *testing.T) {
	allowed := map[string]bool{"errors": true, "fmt": true}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		ast, err := parser.ParseFile(token.NewFileSet(), f, src, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range ast.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if !allowed[path] {
				t.Errorf("%s imports %q; internal/stage must stay pure", f, path)
			}
		}
	}
}
