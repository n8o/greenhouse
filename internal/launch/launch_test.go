package launch

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/n8o/greenhouse/internal/config"
	"github.com/n8o/greenhouse/internal/stage"
)

func testLauncher(d *FakeDeck) *Launcher {
	l := New(config.Config{Repo: "/repo", Stages: map[string]config.Stage{
		"spec":      {Model: "sonnet", Effort: "low", MaxTurns: 5},
		"implement": {Model: "opus", Effort: "high", MaxTurns: 80},
	}}, d)
	l.PromptDir = promptDir
	l.DeliveryPolls = 3
	l.sleep = func(time.Duration) {}
	return l
}

var (
	idle    = State{Status: "waiting", Substate: "idle-at-empty-prompt", ClaudeSessionID: "c-1"}
	working = State{Status: "running", ClaudeSessionID: "c-1"}
	// agent-deck falls back to another conversation in the same directory
	// before the session has a transcript.
	foreign = Output{ClaudeSessionID: "someone-else", Content: "an unrelated reply"}
	own     = Output{ClaudeSessionID: "c-1", Content: EventPrefix + ` {"kind":"spec-done"}` + "\n" + done}
)

func TestLaunchPinsModelAndEffort(t *testing.T) {
	d := &FakeDeck{States: map[string][]State{"s1": {working}}}
	run, err := testLauncher(d).Launch(context.Background(), req(stage.Spec))
	if err != nil {
		t.Fatal(err)
	}
	a := d.Launched[0]
	if a.Model != "sonnet" || a.Effort != "low" {
		t.Errorf("launched model %q effort %q, want the spec stage's sonnet/low", a.Model, a.Effort)
	}
	args := strings.Join(a.Args(), " ")
	for _, w := range []string{"-model sonnet", "-effort low", "-auto-mode", "-assert-done", "-worktree feature/x-1-spec -b", "-g greenhouse", "-c claude"} {
		if !strings.Contains(args, w) {
			t.Errorf("launch args %q lack %q", args, w)
		}
	}
	if run.ID != "s1" || run.Bead != "x-1" || run.Stage != stage.Spec || run.Resent {
		t.Errorf("Run = %+v", run)
	}
	if !strings.Contains(d.Prompts[0], "5 turns") || !strings.Contains(d.Prompts[0], "x-1") {
		t.Errorf("launched prompt is not the rendered spec prompt:\n%s", d.Prompts[0])
	}
}

func TestLaunchRefusesUnpinnedSession(t *testing.T) {
	d := &FakeDeck{NextSession: Session{ID: "s1", Model: "fable", Effort: "xhigh"}}
	_, err := testLauncher(d).Launch(context.Background(), req(stage.Spec))
	if err == nil || !strings.Contains(err.Error(), "want the pinned") {
		t.Fatalf("Launch = %v, want a pinning error", err)
	}
}

func TestLaunchNeedsStageBudget(t *testing.T) {
	d := &FakeDeck{}
	if _, err := testLauncher(d).Launch(context.Background(), req(stage.Learn)); err == nil {
		t.Fatal("a stage without a configured budget must not launch")
	}
	if len(d.Launched) != 0 {
		t.Error("nothing may be launched")
	}
}

func TestLaunchConfirmsDelivery(t *testing.T) {
	tests := []struct {
		name       string
		states     []State
		outputs    []Output
		wantResent bool
		wantErr    error
	}{
		{"working on the prompt", []State{{Status: "starting"}, working}, nil, false, nil},
		{"already replied", []State{idle}, []Output{own}, false, nil},
		{"lost, then delivered on resend", []State{idle, idle, idle, working}, []Output{foreign}, true, nil},
		{"lost twice", []State{idle}, []Output{foreign}, true, ErrNotDelivered},
		{"still starting", []State{{Status: "starting"}}, nil, true, ErrNotDelivered},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &FakeDeck{
				States:  map[string][]State{"s1": tt.states},
				Outputs: map[string][]Output{"s1": tt.outputs},
			}
			run, err := testLauncher(d).Launch(context.Background(), req(stage.Spec))
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Launch error = %v, want %v", err, tt.wantErr)
			}
			if run.Resent != tt.wantResent {
				t.Errorf("Resent = %v, want %v", run.Resent, tt.wantResent)
			}
			wantSends := 0
			if tt.wantResent {
				wantSends = 1
				if d.Prompts[1] != d.Prompts[0] {
					t.Error("the resend must carry the same prompt")
				}
			}
			if len(d.Sends) != wantSends {
				t.Errorf("sends = %d, want %d (resend once at most)", len(d.Sends), wantSends)
			}
		})
	}
}

func TestLaunchFailsOnDeadSession(t *testing.T) {
	d := &FakeDeck{States: map[string][]State{"s1": {{Status: "error"}}}}
	if _, err := testLauncher(d).Launch(context.Background(), req(stage.Spec)); err == nil || !strings.Contains(err.Error(), "error") {
		t.Fatalf("Launch = %v, want an error for a dead session", err)
	}
	if len(d.Sends) != 0 {
		t.Error("a dead session must not be resent to")
	}
}

func TestResult(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name     string
		state    State
		out      Output
		wantDone bool
		want     stage.Event
		wantErr  bool
	}{
		{"working", working, Output{}, false, stage.Event{}, false},
		{"finished", idle, own, true, stage.Event{Kind: stage.SpecDone}, false},
		{"stopped without contract", idle, Output{ClaudeSessionID: "c-1", Content: "I think I am done."}, true, stage.Event{}, true},
		{"no reply of its own", idle, foreign, true, stage.Event{}, true},
		{"stuck on a permission prompt", State{Status: "waiting", Substate: "interactive-menu", ClaudeSessionID: "c-1"},
			Output{ClaudeSessionID: "c-1", Content: "Let me load context."}, true, stage.Event{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &FakeDeck{
				States:  map[string][]State{"s1": {tt.state}},
				Outputs: map[string][]Output{"s1": {tt.out}},
			}
			ev, finished, err := testLauncher(d).Result(ctx, "s1", stage.Spec)
			if finished != tt.wantDone || ev != tt.want || (err != nil) != tt.wantErr {
				t.Fatalf("Result = %+v, %v, %v", ev, finished, err)
			}
			if err != nil && !errors.Is(err, ErrNoResult) && !errors.Is(err, ErrBlocked) {
				t.Errorf("error %v wraps neither ErrNoResult nor ErrBlocked", err)
			}
			if tt.state.Blocked() && !errors.Is(err, ErrBlocked) {
				t.Errorf("a session on a permission prompt must be ErrBlocked, got %v", err)
			}
		})
	}
}

func TestCompletions(t *testing.T) {
	d := &FakeDeck{
		Kids: map[string][]Child{"sched": {
			{ID: "s1", Title: Title("x-1", stage.Spec), Status: "waiting"},
			{ID: "s2", Title: Title("x-2", stage.Implement), Status: "running"},
			{ID: "s3", Title: "someone's own session", Status: "waiting"},
			{ID: "s4", Title: Title("x-4", stage.Implement), Status: "waiting"},
		}},
		States:  map[string][]State{"s1": {idle}, "s4": {{Status: "waiting", ClaudeSessionID: "c-4"}}},
		Outputs: map[string][]Output{"s1": {own}, "s4": {{ClaudeSessionID: "c-4", Content: "no contract"}}},
	}
	l := testLauncher(d)
	if _, err := l.Completions(context.Background()); err == nil {
		t.Error("Completions without a parent must fail")
	}
	l.Parent = "sched"
	got, err := l.Completions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("Completions = %+v, want s1 and s4", got)
	}
	if c := got[0]; c.SessionID != "s1" || c.Bead != "x-1" || c.Stage != stage.Spec || c.Event.Kind != stage.SpecDone || c.Err != nil {
		t.Errorf("completion 0 = %+v", c)
	}
	if c := got[1]; c.SessionID != "s4" || !errors.Is(c.Err, ErrNoResult) {
		t.Errorf("completion 1 = %+v, want a contract error", c)
	}
}

func TestTitleRoundTrip(t *testing.T) {
	for _, st := range []stage.Stage{stage.Spec, stage.Implement, stage.Learn} {
		bead, got, ok := ParseTitle(Title("gr-boot-3", st))
		if !ok || bead != "gr-boot-3" || got != st {
			t.Errorf("ParseTitle(Title(gr-boot-3, %s)) = %q %q %v", st, bead, got, ok)
		}
	}
	for _, bad := range []string{"", "gh-", "gh-spec", "gh-spec-", "gh-nope-x-1", "greenhouse-gr-boot-3"} {
		if _, _, ok := ParseTitle(bad); ok {
			t.Errorf("ParseTitle(%q) ok, want not a greenhouse title", bad)
		}
	}
}
