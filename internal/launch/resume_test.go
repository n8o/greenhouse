package launch

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/n8o/greenhouse/internal/stage"
)

// The first attempt's completion, still the session's last reply when its
// stage runs again.
var (
	firstDone = Output{ClaudeSessionID: "c-1", Timestamp: "2026-10-15T10:57:16Z",
		Content: EventPrefix + ` {"kind":"implement-done","pr":7}` + "\n" + done}
	retryDone = Output{ClaudeSessionID: "c-1", Timestamp: "2026-10-15T11:20:00Z",
		Content: EventPrefix + ` {"kind":"implement-done","pr":7}` + "\n" + done}
)

func retryReq() Request {
	r := req(stage.Implement)
	r.Reason = "--- FAIL: TestX"
	return r
}

func TestPriorFresh(t *testing.T) {
	p := PriorOf(firstDone)
	tests := []struct {
		name string
		p    Prior
		out  Output
		want bool
	}{
		{"first attempt: any reply counts", Prior{}, firstDone, true},
		{"the previous completion is stale", p, firstDone, false},
		{"a later reply with the same text is fresh", p, retryDone, true},
		{"an earlier timestamp is stale", PriorOf(retryDone), firstDone, false},
		{"no timestamps: the same text is stale", PriorOf(Output{Content: "a"}), Output{Content: "a"}, false},
		{"no timestamps: other text is fresh", PriorOf(Output{Content: "a"}), Output{Content: "b"}, true},
	}
	for _, tt := range tests {
		if got := tt.p.Fresh(tt.out); got != tt.want {
			t.Errorf("%s: Fresh = %v, want %v", tt.name, got, tt.want)
		}
	}
	if !PriorOf(Output{}).IsZero() {
		t.Error("a session with no reply has no prior")
	}
}

func TestFindTakesPriorBeforeAnythingIsSent(t *testing.T) {
	d := &FakeDeck{
		Sessions: []Listed{{ID: "s1", Title: Title("x-1", stage.Implement), Group: Group, Status: "stopped", ClaudeSessionID: "c-1"}},
		Outputs:  map[string][]Output{"s1": {firstDone}},
	}
	l := testLauncher(d)
	ex, ok, err := l.Find(context.Background(), "x-1", stage.Implement)
	if err != nil || !ok {
		t.Fatalf("Find = %v, %v", ok, err)
	}
	if ex.ID != "s1" || ex.Prior != PriorOf(firstDone) {
		t.Errorf("Find = %+v, want s1 with the first attempt's reply as prior", ex)
	}
	if _, ok, _ := l.Find(context.Background(), "x-2", stage.Implement); ok {
		t.Error("no session is titled for x-2")
	}

	// Another conversation's reply is not this session's prior.
	d.Outputs["s1"] = []Output{foreign}
	if ex, _, _ := l.Find(context.Background(), "x-1", stage.Implement); !ex.Prior.IsZero() {
		t.Errorf("prior %+v from a foreign conversation", ex.Prior)
	}
	// Without its last reply, the baseline is unknown: no guessing.
	d.OutErr = errors.New("boom")
	if _, _, err := l.Find(context.Background(), "x-1", stage.Implement); err == nil {
		t.Error("Find must fail when the last reply cannot be read")
	}
}

func TestResume(t *testing.T) {
	ex := func(status string) Existing {
		return Existing{Listed: Listed{ID: "s1", Title: Title("x-1", stage.Implement), Status: status, ClaudeSessionID: "c-1"},
			Prior: PriorOf(firstDone)}
	}
	tests := []struct {
		name        string
		status      string
		states      []State
		outputs     []Output
		wantStarted bool
		wantSends   int
		wantErr     error
		wantAdopted bool
	}{
		{"stopped: start, then send", "stopped", []State{working}, nil, true, 1, nil, false},
		{"idle: send without a start", "waiting", []State{working}, nil, false, 1, nil, false},
		{"already working: adopted", "running", nil, nil, false, 0, nil, true},
		// The previous completion is still the last reply: it must not
		// pass for delivery, so the prompt is resent once, then given up.
		{"stale reply is not delivery", "stopped", []State{idle}, []Output{firstDone}, true, 2, ErrNotDelivered, false},
		{"fresh reply is delivery", "stopped", []State{idle}, []Output{retryDone}, true, 1, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &FakeDeck{States: map[string][]State{"s1": tt.states}, Outputs: map[string][]Output{"s1": tt.outputs}}
			run, err := testLauncher(d).Resume(context.Background(), retryReq(), ex(tt.status))
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Resume error = %v, want %v", err, tt.wantErr)
			}
			if len(d.Launched) != 0 {
				t.Error("a resume must never launch")
			}
			if got := slices.Equal(d.Started, []string{"s1"}); got != tt.wantStarted || run.Restarted != tt.wantStarted {
				t.Errorf("started %v, Restarted %v, want %v", d.Started, run.Restarted, tt.wantStarted)
			}
			if len(d.Sends) != tt.wantSends || run.Adopted != tt.wantAdopted || run.Sent != (tt.wantSends > 0) {
				t.Errorf("sends %v, run %+v", d.Sends, run)
			}
			if !run.Reused || run.ID != "s1" {
				t.Errorf("run %+v must be the reused s1", run)
			}
			if tt.wantSends > 0 {
				p := d.Prompts[0]
				for _, w := range []string{"(retry)", "--- FAIL: TestX", "feature/x-1-implement", "**same** PR number", EventPrefix, "80 turns"} {
					if !strings.Contains(p, w) {
						t.Errorf("retry prompt lacks %q:\n%s", w, p)
					}
				}
			}
		})
	}
}

func TestResumeStartFails(t *testing.T) {
	d := &FakeDeck{StartErr: errors.New("tmux gone")}
	run, err := testLauncher(d).Resume(context.Background(), retryReq(),
		Existing{Listed: Listed{ID: "s1", Status: "stopped"}})
	if err == nil || run.Sent || len(d.Sends) != 0 {
		t.Fatalf("Resume = %+v, %v; want a start error and nothing sent", run, err)
	}
}

func TestCollectIgnoresPreviousCompletion(t *testing.T) {
	ctx := context.Background()
	prior := PriorOf(firstDone)
	d := &FakeDeck{States: map[string][]State{"s1": {idle}}, Outputs: map[string][]Output{"s1": {firstDone}}}
	l := testLauncher(d)
	if _, _, done, err := l.Collect(ctx, "s1", stage.Implement, prior); done || err != nil {
		t.Fatalf("Collect of the previous completion = done %v, %v; want still waiting", done, err)
	}
	// Without a prior (a first attempt) the same reply is the result.
	if ev, _, done, err := l.Collect(ctx, "s1", stage.Implement, Prior{}); !done || err != nil || ev.PR != 7 {
		t.Fatalf("first-attempt Collect = %+v, %v, %v", ev, done, err)
	}
	d.Outputs["s1"] = []Output{retryDone}
	ev, _, done, err := l.Collect(ctx, "s1", stage.Implement, prior)
	if !done || err != nil || ev.Kind != stage.ImplementDone || ev.PR != 7 {
		t.Fatalf("Collect of the fresh completion = %+v, %v, %v", ev, done, err)
	}
}

func TestRenderRetryNeedsReason(t *testing.T) {
	r := req(stage.Spec)
	r.Reason = ""
	if _, err := RenderRetry(promptDir, r, 5); err == nil {
		t.Error("a retry without a reason must not render")
	}
	r.Reason = "a usage limit stopped you"
	p, err := RenderRetry(promptDir, r, 5)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p, "a usage limit stopped you") || strings.Contains(p, "PR number") || !strings.Contains(p, `{"kind":"spec-done"}`) {
		t.Errorf("spec retry prompt:\n%s", p)
	}
}
