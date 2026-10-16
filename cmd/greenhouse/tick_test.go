package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/n8o/greenhouse/internal/config"
	"github.com/n8o/greenhouse/internal/gate"
	"github.com/n8o/greenhouse/internal/launch"
	"github.com/n8o/greenhouse/internal/ledger"
	"github.com/n8o/greenhouse/internal/plan"
	"github.com/n8o/greenhouse/internal/verify"
)

const parent = "boss"

// testDeck is a FakeDeck whose launches get distinct ids and show up as
// running children of parent, so several ticks can be driven against it.
type testDeck struct {
	launch.FakeDeck
	n       int
	replies int // finished turns, for reply timestamps
}

func (d *testDeck) Launch(ctx context.Context, a launch.LaunchArgs) (launch.Session, error) {
	d.n++
	d.NextSession = launch.Session{ID: fmt.Sprintf("s%d", d.n)}
	s, err := d.FakeDeck.Launch(ctx, a)
	if err != nil {
		return s, err
	}
	d.States[s.ID] = []launch.State{{ID: s.ID, Status: "running"}}
	d.Kids[parent] = append(d.Kids[parent], launch.Child{ID: s.ID, Title: a.Title, Status: "running"})
	return s, nil
}

// Send records the message and, like a real session, starts a turn on it.
func (d *testDeck) Send(ctx context.Context, id, messageFile string) error {
	if err := d.FakeDeck.Send(ctx, id, messageFile); err != nil {
		return err
	}
	d.States[id] = []launch.State{{ID: id, Status: "running", ClaudeSessionID: "c-" + id}}
	for i, k := range d.Kids[parent] {
		if k.ID == id {
			d.Kids[parent][i].Status = "running"
		}
	}
	return nil
}

// finish makes session id end its turn with content as its final message,
// stamped later than any message before it.
func (d *testDeck) finish(id, content string) {
	d.replies++
	at := time.Date(2026, 10, 12, 9, 0, d.replies, 0, time.UTC).Format(time.RFC3339Nano)
	d.States[id] = []launch.State{{ID: id, Status: "waiting", ClaudeSessionID: "c-" + id}}
	d.Outputs[id] = []launch.Output{{ClaudeSessionID: "c-" + id, Content: content, Timestamp: at}}
	for i, k := range d.Kids[parent] {
		if k.ID == id {
			d.Kids[parent][i].Status = "waiting"
		}
	}
}

func result(event string) string {
	return "Done.\nGREENHOUSE-EVENT: " + event + "\n===AGENTDECK_DONE=== status=ok summary=x\n"
}

type fakeGH struct {
	prs   map[int]*PR
	label bool // the repo has the greenhouse label
	calls []string
}

func (g *fakeGH) EnsureLabel(context.Context) error {
	if !g.label {
		g.label = true
		g.calls = append(g.calls, "label create")
	}
	return nil
}

func (g *fakeGH) PR(_ context.Context, n int) (PR, error) {
	pr, ok := g.prs[n]
	if !ok {
		return PR{}, fmt.Errorf("no PR %d", n)
	}
	return *pr, nil
}

func (g *fakeGH) AddLabel(_ context.Context, n int) error {
	g.calls = append(g.calls, fmt.Sprintf("add-label %d", n))
	if !slices.Contains(g.prs[n].Labels, gate.PRLabel) {
		g.prs[n].Labels = append(g.prs[n].Labels, gate.PRLabel)
	}
	return nil
}

func (g *fakeGH) Ready(_ context.Context, n int) error {
	g.calls = append(g.calls, fmt.Sprintf("ready %d", n))
	g.prs[n].IsDraft = false
	return nil
}

type world struct {
	t      *testing.T
	plan   *plan.Fake
	deck   *testDeck
	gh     *fakeGH
	probe  *gate.Fake
	runner *verify.Fake
	clock  time.Time
	out    bytes.Buffer
	s      *scheduler
}

func newWorld(t *testing.T) *world {
	t.Helper()
	cfg, err := config.Load(repoConfig)
	if err != nil {
		t.Fatal(err)
	}
	prompts, _ := filepath.Abs(filepath.Join("..", "..", "prompts"))
	cfg.Repo = t.TempDir()
	cfg.Run.Parent = parent
	w := &world{t: t,
		plan: &plan.Fake{Beads: []plan.Bead{
			{ID: "p-1", Title: "First step", Status: "open", Description: "Done when: it works.",
				Labels: []string{plan.ApprovedLabel}},
			{ID: "p-2", Title: "Second step", Status: "open", Labels: []string{plan.ApprovedLabel},
				Dependencies: []plan.Dependency{{DependsOn: "p-1", Type: "blocks"}}},
		}},
		deck: &testDeck{FakeDeck: launch.FakeDeck{States: map[string][]launch.State{},
			Outputs: map[string][]launch.Output{}, Kids: map[string][]launch.Child{}}},
		gh:    &fakeGH{prs: map[int]*PR{}},
		probe: &gate.Fake{},
		// The PR closes p-1's line and nothing else (ADR-0005 plan scope).
		runner: &verify.Fake{Replies: map[string]verify.Reply{
			"git merge-base origin/main HEAD":                               {Out: "abc\n"},
			"git diff --name-status --no-renames abc HEAD -- .beads/plans/": {Out: "M\t.beads/plans/p.jsonl\n"},
			"git show abc:.beads/plans/p.jsonl":                             {Out: `{"id":"p-1","status":"open"}` + "\n"},
			"git show HEAD:.beads/plans/p.jsonl":                            {Out: `{"id":"p-1","status":"closed","close_reason":"this PR"}` + "\n"},
		}},
		clock: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC),
	}
	w.s, err = newScheduler(cfg, adapters{plan: w.plan, deck: w.deck, gh: w.gh, probe: w.probe,
		runner: w.runner, now: func() time.Time { return w.clock }}, &w.out, false)
	if err != nil {
		t.Fatal(err)
	}
	w.s.launcher.PromptDir = prompts
	w.s.launcher.PollInterval = time.Millisecond
	w.s.launcher.DeliveryPolls = 3
	w.s.local = time.UTC
	return w
}

func (w *world) tick() {
	w.t.Helper()
	w.out.Reset()
	if err := w.s.Tick(context.Background()); err != nil {
		w.t.Fatalf("tick: %v\n%s", err, w.out.String())
	}
}

func (w *world) stage(id string) string {
	for _, b := range w.plan.Beads {
		if b.ID == id {
			return b.Stage()
		}
	}
	return "?"
}

func (w *world) records() []ledger.Record {
	recs, err := w.s.store.Records()
	if err != nil {
		w.t.Fatal(err)
	}
	return recs
}

func outcomes(recs []ledger.Record) string {
	var s []string
	for _, r := range recs {
		s = append(s, r.Stage+":"+r.Outcome)
	}
	return strings.Join(s, " ")
}

// snapshot is every externally visible effect, to prove a tick did nothing.
func (w *world) snapshot() string {
	run, _ := w.s.store.Run("p-1")
	reuse := "none"
	if run != nil && run.Reuse != nil {
		r := *run
		reuse = fmt.Sprintf("%+v", *r.Reuse)
		r.Reuse = nil // print the value, not the pointer
		run = &r
	}
	return fmt.Sprintf("writes=%v launched=%d stopped=%v started=%v sends=%v gh=%v ledger=%d run=%+v reuse=%s",
		w.plan.Writes, len(w.deck.Launched), w.deck.Stopped, w.deck.Started, w.deck.Sends, w.gh.calls, len(w.records()), run, reuse)
}

// idle ticks once more and fails if anything changed.
func (w *world) idle() {
	w.t.Helper()
	before := w.snapshot()
	w.tick()
	if after := w.snapshot(); after != before {
		w.t.Errorf("a repeated tick changed state:\nbefore %s\nafter  %s\n%s", before, after, w.out.String())
	}
}

func (w *world) want(cond bool, format string, args ...any) {
	w.t.Helper()
	if !cond {
		w.t.Errorf(format+"\n%s", append(args, w.out.String())...)
	}
}

// toVerify drives p-1 through spec and implement to an open draft PR 7.
func (w *world) toVerify() {
	w.t.Helper()
	w.gh.prs[7] = &PR{Number: 7, State: "OPEN", IsDraft: true, HeadRef: "feature/p-1-implement"}
	w.tick() // start → spec session s1
	w.deck.finish("s1", result(`{"kind":"spec-done"}`))
	w.tick() // collect s1 → implement session s2
	w.deck.finish("s2", result(`{"kind":"implement-done","pr":7}`))
}

func TestTickHappyPath(t *testing.T) {
	w := newWorld(t)
	w.tick()
	w.want(w.stage("p-1") == "spec" && len(w.deck.Launched) == 1, "want p-1 in spec with one launch, got %q %d", w.stage("p-1"), len(w.deck.Launched))
	w.want(w.deck.Launched[0].Title == "gh-spec-p-1" && w.deck.Launched[0].Parent == parent, "launch %+v", w.deck.Launched[0])
	w.want(w.stage("p-2") == "", "p-2 is blocked by p-1 and must not start")
	w.idle()

	w.deck.finish("s1", result(`{"kind":"spec-done"}`))
	w.tick()
	w.want(w.stage("p-1") == "implement" && len(w.deck.Launched) == 2, "want implement launched, got %q", w.stage("p-1"))
	w.want(slices.Equal(w.deck.Stopped, []string{"s1"}), "collected spec session must be stopped, stopped %v", w.deck.Stopped)
	w.idle()

	w.gh.prs[7] = &PR{Number: 7, State: "OPEN", IsDraft: true, HeadRef: "feature/p-1-implement"}
	// The session closes its own bead inside its PR (ADR-0005); with a
	// shared bd database that close is visible before the merge.
	w.plan.Close(context.Background(), "p-1", "this PR")
	w.deck.finish("s2", result(`{"kind":"implement-done","pr":7}`))
	w.tick()
	pr := w.gh.prs[7]
	w.want(w.stage("p-2") == "", "p-2 must wait for p-1's merge, not its in-PR close")
	w.want(w.stage("p-1") == "review", "want review, got %q", w.stage("p-1"))
	w.want(!pr.IsDraft && slices.Contains(pr.Labels, gate.PRLabel) && w.gh.label, "PR must be labelled and ready: %+v", pr)
	w.want(slices.Contains(w.runner.Calls, "git -c user.name=greenhouse -c user.email=greenhouse@invalid rebase origin/main"),
		"verify must rebase onto origin/main: %v", w.runner.Calls)
	w.want(slices.Contains(w.runner.Calls, "sh -c go test ./..."), "verify commands not run: %v", w.runner.Calls)
	w.idle()

	pr.State = "MERGED"
	w.tick()
	w.want(w.plan.Beads[0].Status == "closed" && w.stage("p-1") == "done", "merged bead must close: %+v", w.plan.Beads[0])
	if got, want := outcomes(w.records()), "spec:spec-done implement:implement-done verify:verify-pass review:merged"; got != want {
		t.Errorf("ledger %q, want %q", got, want)
	}
	// p-1 closed unblocks p-2, which starts on this same tick.
	w.want(w.stage("p-2") == "spec", "p-2 must start once p-1 closes, got %q", w.stage("p-2"))
}

func TestTickVerifyFailRetryThenPark(t *testing.T) {
	w := newWorld(t)
	w.runner.Replies["sh -c go test ./..."] = verify.Reply{Out: "--- FAIL: TestX", Err: errors.New("exit status 1")}
	w.toVerify()
	w.tick() // verify fails, retry: the implement session s2 again
	w.want(w.stage("p-1") == "implement" && len(w.deck.Launched) == 2, "want an implement retry in s2, got %q, %d launches", w.stage("p-1"), len(w.deck.Launched))
	w.want(slices.Equal(w.deck.Started, []string{"s2"}) && slices.Equal(w.deck.Sends, []string{"s2"}),
		"the collected (stopped) s2 must be started again and sent the retry: started %v, sends %v", w.deck.Started, w.deck.Sends)
	retry := w.deck.Prompts[len(w.deck.Prompts)-1]
	w.want(strings.Contains(retry, "--- FAIL: TestX") && strings.Contains(retry, "(retry)") && strings.Contains(retry, "**same** PR number"),
		"the verify log must reach the retry prompt:\n%s", retry)
	run, _ := w.s.store.Run("p-1")
	w.want(run != nil && run.Session == "s2" && run.Retries == 1 && run.Reuse != nil && run.Reuse.Sent && !run.Reuse.PriorAt.IsZero(),
		"open run must reuse s2 with retries 1 and the prior reply's time: %+v", run)
	w.idle()

	// The session stops working, but its last reply is still the first
	// attempt's implement-done: that is not this run's result.
	w.deck.States["s2"] = []launch.State{{ID: "s2", Status: "waiting", ClaudeSessionID: "c-s2"}}
	w.deck.Kids[parent][1].Status = "waiting"
	w.idle()
	w.want(w.stage("p-1") == "implement" && len(w.records()) == 3, "a stale completion must not be collected: %s", outcomes(w.records()))

	w.deck.finish("s2", result(`{"kind":"implement-done","pr":7}`))
	w.tick() // the fresh completion is collected; verify fails again: park
	impl := w.records()[3]
	w.want(impl.Stage == "implement" && impl.Session == "s2" && impl.Retries == 1 && impl.Outcome == "implement-done",
		"the retry's record: %+v", impl)
	w.want(w.stage("p-1") == "parked", "want parked after one retry, got %q", w.stage("p-1"))
	recs := w.records()
	var retries []int
	for _, r := range recs {
		if r.Stage == "verify" {
			retries = append(retries, r.Retries)
		}
	}
	w.want(slices.Equal(retries, []int{0, 1}), "verify retries %v, want [0 1]", retries)
	w.want(w.gh.prs[7].IsDraft, "a failing PR must stay a draft")
	w.idle()
}

// failVerify drives p-1 to a failed verify of PR 7, collected and stopped
// implement session s2 included, without ticking the retry.
func (w *world) failVerify() {
	w.t.Helper()
	w.runner.Replies["sh -c go test ./..."] = verify.Reply{Out: "--- FAIL: TestX", Err: errors.New("exit status 1")}
	w.toVerify()
}

func TestTickRetryWithoutSessionLaunchesOnTheBranch(t *testing.T) {
	w := newWorld(t)
	w.failVerify()
	// One tick collects s2, verifies, and starts the retry in s2. Replay
	// the retry as if the tick had crashed before sending, and a human had
	// then removed s2: no session holds the title any more.
	w.tick()
	run, _ := w.s.store.Run("p-1")
	run.Reuse.Sent = false
	w.s.store.PutRun(*run)
	w.deck.Kids[parent] = slices.DeleteFunc(w.deck.Kids[parent], func(k launch.Child) bool { return k.ID == "s2" })
	w.tick()
	w.want(len(w.deck.Launched) == 3 && w.deck.Launched[2].Title == "gh-implement-p-1" && !w.deck.Launched[2].NewBranch,
		"want a new implement session on the existing branch: %+v", w.deck.Launched)
	w.want(strings.Contains(w.deck.Prompts[len(w.deck.Prompts)-1], "Previous attempt failed verification"),
		"a new session gets the implement prompt with the verify log")
	run, _ = w.s.store.Run("p-1")
	w.want(run != nil && run.Session == "s3" && run.Reuse == nil && run.Retries == 1, "open run %+v", run)
	w.idle()
}

func TestTickRetryCrashBeforeSendRecorded(t *testing.T) {
	w := newWorld(t)
	w.failVerify()
	w.tick() // the retry is sent to s2, which is now working on it
	// Crash: the send happened but the open run never recorded it.
	run, _ := w.s.store.Run("p-1")
	prior := *run.Reuse
	run.Reuse.Sent = false
	w.s.store.PutRun(*run)
	w.tick()
	run, _ = w.s.store.Run("p-1")
	w.want(slices.Equal(w.deck.Sends, []string{"s2"}) && len(w.deck.Started) == 1,
		"a working session is adopted, not sent the retry again: sends %v, started %v", w.deck.Sends, w.deck.Started)
	w.want(run.Reuse.Sent && run.Reuse.PriorAt.Equal(prior.PriorAt) && run.Reuse.PriorDigest == prior.PriorDigest,
		"the adopted run keeps the baseline taken before the send: %+v vs %+v", *run.Reuse, prior)
	w.idle()
}

func TestTickRetryStartFailureParks(t *testing.T) {
	w := newWorld(t)
	w.failVerify()
	w.deck.StartErr = errors.New("tmux: no server")
	w.tick()
	w.want(w.stage("p-1") == "parked", "an unresumable session parks the bead, got %q", w.stage("p-1"))
	last := w.records()[len(w.records())-1]
	w.want(last.Stage == "implement" && last.Outcome == "needs-human" && last.Session == "s2" && last.Retries == 1 &&
		strings.Contains(last.Reason, "could not resume the implement session s2") && strings.Contains(last.Reason, "tmux: no server"),
		"record %+v", last)
	w.want(len(w.deck.Sends) == 0 && len(w.deck.Launched) == 2, "nothing sent or launched")
	w.idle()
}

func TestTickRetryDryRun(t *testing.T) {
	w := newWorld(t)
	w.failVerify()
	w.tick() // the retry resumes s2
	run, _ := w.s.store.Run("p-1")
	run.Reuse.Sent = false // as if the tick crashed before the send
	w.s.store.PutRun(*run)
	w.deck.States["s2"] = []launch.State{{ID: "s2", Status: "waiting", ClaudeSessionID: "c-s2"}}
	w.deck.Kids[parent][1].Status = "waiting"
	w.s.dry = true
	w.idle()
	w.want(strings.Contains(w.out.String(), "would resume gh-implement-p-1"), "dry run must report the resume")
}

func TestTickNeedsHumanParks(t *testing.T) {
	w := newWorld(t)
	w.tick()
	w.deck.finish("s1", result(`{"kind":"needs-human","question":"Which API? I recommend the CLI."}`))
	w.tick()
	w.want(w.stage("p-1") == "parked", "want parked, got %q", w.stage("p-1"))
	last := w.records()[0]
	w.want(last.Outcome == "needs-human" && strings.Contains(last.Reason, "Which API?"), "ledger %+v", last)
	w.idle()

	// The human unparks by removing the label: the bead restarts at spec.
	w.plan.SetStage(context.Background(), "p-1", "")
	w.tick()
	// Its spec session still exists, so spec runs again in it (ADR-0009).
	w.want(w.stage("p-1") == "spec" && len(w.deck.Launched) == 1 && slices.Equal(w.deck.Sends, []string{"s1"}),
		"unparked bead must restart at spec in its spec session, got %q, %d launches, sends %v", w.stage("p-1"), len(w.deck.Launched), w.deck.Sends)
	w.want(strings.Contains(w.deck.Prompts[1], "its last run was spec, needs-human: Which API?"), "the retry says why:\n%s", w.deck.Prompts[1])
	w.idle()
}

func TestTickCrashConverges(t *testing.T) {
	w := newWorld(t)
	w.tick()

	// Crash 1: the launch succeeded but the tick died before recording the
	// session on the open run.
	run, _ := w.s.store.Run("p-1")
	run.Session = ""
	w.s.store.PutRun(*run)
	w.tick()
	run, _ = w.s.store.Run("p-1")
	w.want(len(w.deck.Launched) == 1 && run.Session == "s1", "the running session must be adopted, not relaunched: %d launches, run %+v", len(w.deck.Launched), run)

	// Crash 2: the outcome is in the ledger but the label never moved.
	w.deck.finish("s1", result(`{"kind":"spec-done"}`))
	w.plan.WriteErr = errors.New("bd: killed")
	if err := w.s.Tick(context.Background()); err == nil {
		t.Fatal("want the injected bd failure")
	}
	w.want(w.stage("p-1") == "spec" && len(w.records()) == 1, "crash left stage %q, %d records", w.stage("p-1"), len(w.records()))
	w.plan.WriteErr = nil
	w.tick()
	w.want(w.stage("p-1") == "implement" && len(w.deck.Launched) == 2 && len(w.records()) == 1,
		"after the crash: stage %q, %d launches, %d records", w.stage("p-1"), len(w.deck.Launched), len(w.records()))
	w.idle()
}

func TestTickGateDeniesLaunch(t *testing.T) {
	w := newWorld(t)
	week := &gate.Window{UsedPercentage: 90}
	w.probe.Reading = &gate.Reading{SevenDay: week, UpdatedAt: w.clock}
	before := w.snapshot()
	w.tick()
	w.want(w.snapshot() == before && w.stage("p-1") == "", "a denied gate must change nothing")
	w.want(strings.Contains(w.out.String(), "gate quota: deny: 7-day usage 90%"), "the verdict must be printed")

	// Implement launches also respect the review backlog.
	w.probe.Reading = nil
	w.tick()
	w.probe.Open = 3
	w.deck.finish("s1", result(`{"kind":"spec-done"}`))
	w.tick()
	w.want(w.stage("p-1") == "implement" && len(w.deck.Launched) == 1, "implement must be held: %q, %d launches", w.stage("p-1"), len(w.deck.Launched))
	w.want(strings.Contains(w.out.String(), "gate backlog: deny"), "backlog verdict missing")
	w.probe.Open = 0
	w.tick()
	w.want(len(w.deck.Launched) == 2, "implement must launch once the backlog clears")
}

func TestTickTimeout(t *testing.T) {
	w := newWorld(t)
	w.tick()
	w.clock = w.clock.Add(29 * time.Minute)
	w.idle()
	w.clock = w.clock.Add(2 * time.Minute) // past spec's 30m limit
	w.tick()
	w.want(slices.Equal(w.deck.Stopped, []string{"s1"}), "the overrunning session must be stopped: %v", w.deck.Stopped)
	r := w.records()[0]
	w.want(r.TimedOut && r.Outcome == "needs-human" && w.stage("p-1") == "parked", "timeout must fail the run: %+v, stage %q", r, w.stage("p-1"))
}

func TestTickUsageLimitParksLaunches(t *testing.T) {
	w := newWorld(t)
	w.tick()
	w.deck.finish("s1", "You've hit your session limit · resets 11am\n")
	w.tick()
	park, _ := w.s.store.Park()
	w.want(park.Until.Equal(time.Date(2026, 10, 12, 11, 0, 0, 0, time.UTC)), "park %+v", park)
	w.want(w.records()[0].Outcome == ledger.UsageLimit && w.stage("p-1") == "spec", "the run is recorded and the stage kept")
	w.want(len(w.deck.Launched) == 1, "no relaunch while parked")
	w.idle()
	w.clock = park.Until.Add(time.Minute)
	w.tick()
	w.want(len(w.deck.Launched) == 1 && slices.Equal(w.deck.Started, []string{"s1"}) && slices.Equal(w.deck.Sends, []string{"s1"}),
		"spec resumes in its session once the park lifts: %d launches, started %v, sends %v", len(w.deck.Launched), w.deck.Started, w.deck.Sends)
	w.want(strings.Contains(w.deck.Prompts[1], "usage limit stopped your previous turn"), "the retry says why:\n%s", w.deck.Prompts[1])
}

func TestTickDryRun(t *testing.T) {
	w := newWorld(t)
	w.tick()
	w.deck.finish("s1", result(`{"kind":"spec-done"}`))
	w.s.dry = true
	w.idle()
	out := w.out.String()
	for _, want := range []string{"dry run", "session s1 finished: spec-done", "would append to ledger: spec spec-done",
		"would stop session s1", "[spec] event spec-done → implement; actions [launch]", "would label stage:implement"} {
		w.want(strings.Contains(out, want), "dry-run output missing %q", want)
	}
}

func TestLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.lock")
	unlock, err := lock(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lock(path); !errors.Is(err, errLocked) {
		t.Errorf("second lock: %v, want errLocked", err)
	}
	unlock()
	unlock2, err := lock(path)
	if err != nil {
		t.Errorf("lock after unlock: %v", err)
	} else {
		unlock2()
	}
}
