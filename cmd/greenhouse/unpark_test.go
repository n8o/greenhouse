package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/n8o/greenhouse/internal/ledger"
	"github.com/n8o/greenhouse/internal/plan"
	"github.com/n8o/greenhouse/internal/verify"
)

// parkAtVerify drives p-1 to stage:parked after its verify failed with the
// retry used. As a real implement session does, it closes p-1 in the local
// bd database while preparing its PR (ADR-0005), so p-1 is closed locally
// but its close is not on origin/main.
func (w *world) parkAtVerify() {
	w.t.Helper()
	w.failVerify()
	w.plan.Close(context.Background(), "p-1", "this PR")
	w.tick() // verify fails → implement retry in s2
	w.deck.finish("s2", result(`{"kind":"implement-done","pr":7}`))
	w.tick() // verify fails again → parked
	if w.stage("p-1") != "parked" {
		w.t.Fatalf("setup: want p-1 parked, got %q\n%s", w.stage("p-1"), w.out.String())
	}
}

func (w *world) unparks() []ledger.Record {
	var u []ledger.Record
	for _, r := range w.records() {
		if r.Outcome == ledger.Unparked {
			u = append(u, r)
		}
	}
	return u
}

// TestUnparkClosedLocallyResumesVerify is the observed gap: a bead closed by
// its own implement session in the shared bd database, its PR open and
// unmerged, parked at verify. The human removes stage:parked. The bead must
// not read as done, its dependent must stay blocked, and it resumes verify.
func TestUnparkClosedLocallyResumesVerify(t *testing.T) {
	w := newWorld(t)
	w.parkAtVerify()
	w.want(strings.Contains(w.out.String(), "remove stage:parked to resume verify, or add stage:spec to restart at spec"),
		"the parked line must name the stage it resumes")
	w.want(w.stage("p-2") == "", "p-2 must not start on an unmerged p-1")
	w.idle()

	w.runner.Replies["sh -c go test ./..."] = verify.Reply{} // the human fixed the PR
	w.plan.SetStage(context.Background(), "p-1", "")

	// The next dry-run tick (where the gap was seen) proposes the resume, not p-2.
	w.s.dry = true
	w.idle()
	out := w.out.String()
	for _, want := range []string{
		"closed in the local bd database, but its close is not on origin/main: not done",
		"resume verify, verify retries reset", "would append to ledger: parked unparked (PR #7)",
		"would label stage:verify", "waits on p-1 (closed locally, not on origin/main)",
	} {
		w.want(strings.Contains(out, want), "dry run missing %q", want)
	}
	w.want(!strings.Contains(out, "p-2        start"), "p-2 must not start")
	w.s.dry = false

	w.tick()
	u := w.unparks()
	w.want(len(u) == 1 && u[0].Resume == "verify" && u[0].PR == 7, "unpark record %+v", u)
	w.want(w.stage("p-1") == "review" && !w.gh.prs[7].IsDraft, "p-1 must be re-verified and go to review, got %q", w.stage("p-1"))
	w.want(len(w.deck.Launched) == 2 && len(w.deck.Sends) == 1, "verify launches nothing: %d launches, sends %v", len(w.deck.Launched), w.deck.Sends)
	w.want(w.stage("p-2") == "", "p-2 waits for p-1's merge, got %q", w.stage("p-2"))
	w.idle()

	w.gh.prs[7].State = "MERGED"
	w.land("p-1")
	w.tick()
	w.want(w.stage("p-1") == "done" && w.stage("p-2") == "spec", "merged and landed: p-1 %q, p-2 %q", w.stage("p-1"), w.stage("p-2"))
}

// TestUnparkResetsRetries: the unpark is a ledger record, and a verify that
// fails again after it has its whole retry budget back.
func TestUnparkResetsRetries(t *testing.T) {
	w := newWorld(t)
	w.parkAtVerify()
	w.want(ledger.Retries(w.records(), "p-1") == 2, "two verify failures before the unpark")
	w.plan.SetStage(context.Background(), "p-1", "")
	w.tick()
	recs := w.records()
	last := recs[len(recs)-1]
	w.want(recs[len(recs)-2].Outcome == ledger.Unparked, "the unpark is recorded before the re-verify: %s", outcomes(recs))
	w.want(last.Stage == "verify" && last.Outcome == "verify-fail" && last.Retries == 0,
		"the re-verify runs with the retry budget reset: %+v", last)
	w.want(w.stage("p-1") == "implement" && slices.Equal(w.deck.Sends, []string{"s2", "s2"}),
		"a failure after the unpark retries in s2 instead of parking: %q, sends %v", w.stage("p-1"), w.deck.Sends)
}

// TestUnparkCrashBeforeLabel: the unpark is recorded but the label never
// moved. The next tick finishes the move and does not record it twice.
func TestUnparkCrashBeforeLabel(t *testing.T) {
	w := newWorld(t)
	w.parkAtVerify()
	w.plan.SetStage(context.Background(), "p-1", "")
	w.plan.WriteErr = errors.New("bd: killed")
	if err := w.s.Tick(context.Background()); err == nil {
		t.Fatal("want the injected bd failure")
	}
	w.want(len(w.unparks()) == 1 && w.stage("p-1") == "", "crash after the record: %d unparks, stage %q", len(w.unparks()), w.stage("p-1"))
	w.plan.WriteErr = nil
	w.tick()
	w.want(len(w.unparks()) == 1 && w.stage("p-1") == "implement", "converged: %d unparks, stage %q", len(w.unparks()), w.stage("p-1"))
}

// TestClosedOnMainIsDone: a close that reached origin/main is done, whatever
// the local bd database says, and unblocks its dependent.
func TestClosedOnMainIsDone(t *testing.T) {
	for _, local := range []string{"closed", "open"} {
		t.Run(local, func(t *testing.T) {
			w := newWorld(t)
			w.plan.Beads[0].Status = local
			w.land("p-1")
			w.tick()
			w.want(w.stage("p-1") == "" && w.stage("p-2") == "spec" && w.deck.Launched[0].Title == "gh-spec-p-2",
				"p-1 is done, p-2 starts: p-1 %q, p-2 %q", w.stage("p-1"), w.stage("p-2"))
			w.want(len(w.records()) == 0, "nothing recorded for a done bead")
		})
	}
}

// TestUnparkAtImplementResumesSession: an implement session that asked a
// question parks the bead; removing stage:parked resumes that session (a
// send, not a launch) with the question in the retry prompt.
func TestUnparkAtImplementResumesSession(t *testing.T) {
	w := newWorld(t)
	w.tick()
	w.deck.finish("s1", result(`{"kind":"spec-done"}`))
	w.tick()
	w.deck.finish("s2", result(`{"kind":"needs-human","question":"Which database? I recommend SQLite."}`))
	w.tick()
	w.want(w.stage("p-1") == "parked", "want parked, got %q", w.stage("p-1"))
	w.want(strings.Contains(w.out.String(), "remove stage:parked to resume implement"), "the parked line names implement")
	w.idle()

	w.plan.SetStage(context.Background(), "p-1", "")
	w.tick()
	w.want(w.stage("p-1") == "implement" && len(w.deck.Launched) == 2 && slices.Equal(w.deck.Sends, []string{"s2"}),
		"resume implement in s2: stage %q, %d launches, sends %v", w.stage("p-1"), len(w.deck.Launched), w.deck.Sends)
	prompt := w.deck.Prompts[len(w.deck.Prompts)-1]
	w.want(strings.Contains(prompt, "(retry)") && strings.Contains(prompt, "released it to the implement stage") &&
		strings.Contains(prompt, "Which database?"), "the retry prompt says a human released it, and from what:\n%s", prompt)
	w.idle()
}

// TestUnparkExplicitRestartAtSpec: adding stage:spec to a parked bead (the
// documented "restart from scratch") resumes spec, whatever parked it.
func TestUnparkExplicitRestartAtSpec(t *testing.T) {
	w := newWorld(t)
	w.parkAtVerify()
	w.plan.Beads[0].Labels = append(w.plan.Beads[0].Labels, plan.StagePrefix+"spec")
	w.tick()
	u := w.unparks()
	w.want(len(u) == 1 && u[0].Resume == "spec" && strings.Contains(u[0].Reason, "a human set stage:spec"), "unpark %+v", u)
	w.want(!slices.Contains(w.plan.Beads[0].Labels, "stage:parked") && w.stage("p-1") == "spec",
		"stage:spec is the only stage label: %v", w.plan.Beads[0].Labels)
	w.want(slices.Contains(w.deck.Sends, "s1") && len(w.deck.Launched) == 2, "spec resumes in s1: sends %v", w.deck.Sends)
	w.want(ledger.Retries(w.records(), "p-1") == 0, "retries reset")
	w.idle()

	// Parked by spec itself: adding stage:spec is a release, not the
	// spec → parked transition left unfinished.
	w = newWorld(t)
	w.tick()
	w.deck.finish("s1", result(`{"kind":"needs-human","question":"Which API?"}`))
	w.tick()
	w.want(w.stage("p-1") == "parked", "setup: parked at spec")
	w.plan.Beads[0].Labels = append(w.plan.Beads[0].Labels, plan.StagePrefix+"spec")
	w.tick()
	w.want(w.stage("p-1") == "spec" && len(w.unparks()) == 1 && slices.Equal(w.deck.Sends, []string{"s1"}),
		"spec resumes in s1: stage %q, %d unparks, sends %v", w.stage("p-1"), len(w.unparks()), w.deck.Sends)
	w.idle()
}

// TestClosedLocallyWithoutRunParks: closed locally, not on main, and no run
// in the ledger: neither done nor in flight, so it is parked with a reason
// (an approved bead) or only reported (any other), and its dependent waits.
func TestClosedLocallyWithoutRunParks(t *testing.T) {
	w := newWorld(t)
	w.plan.Beads[0].Status = "closed"
	w.tick()
	w.want(w.stage("p-1") == "parked" && w.stage("p-2") == "", "p-1 %q, p-2 %q", w.stage("p-1"), w.stage("p-2"))
	recs := w.records()
	w.want(len(recs) == 1 && recs[0].Stage == ledger.ParkedStage && recs[0].Outcome == "needs-human" &&
		strings.Contains(recs[0].Reason, "its close is not on origin/main and greenhouse has no run of it"), "park record %+v", recs)
	w.want(len(w.deck.Launched) == 0, "nothing launched")
	w.want(strings.Contains(w.out.String(), "remove stage:parked to start it"), "parked hint")
	w.idle()

	// Unparked while still closed: it parks again rather than guessing.
	w.plan.SetStage(context.Background(), "p-1", "")
	w.tick()
	w.want(w.stage("p-1") == "parked" && len(w.records()) == 2, "re-parked: %q, %d records", w.stage("p-1"), len(w.records()))

	// Reopened and unparked: it starts like a new bead.
	w.plan.Beads[0].Status = "open"
	w.plan.SetStage(context.Background(), "p-1", "")
	w.tick()
	w.want(w.stage("p-1") == "spec" && len(w.deck.Launched) == 1, "reopened bead starts: %q", w.stage("p-1"))

	u := newWorld(t)
	u.plan.Beads[0].Status = "closed"
	u.plan.Beads[0].Labels = nil
	u.tick()
	u.want(u.stage("p-1") == "" && len(u.records()) == 0 && strings.Contains(u.out.String(), "not done; its dependents wait"),
		"an unapproved bead is reported, not parked")
}

// TestUnreadableMainPlanFailsTick: with no plan on origin/main nothing may
// read as done or be parked for it.
func TestUnreadableMainPlanFailsTick(t *testing.T) {
	w := newWorld(t)
	w.plan.Beads[0].Status = "closed"
	w.runner.Replies["git ls-tree --name-only origin/main -- .beads/plans/"] = verify.Reply{}
	before := w.snapshot()
	if err := w.s.Tick(context.Background()); err == nil || !strings.Contains(err.Error(), "no plan under .beads/plans/") {
		t.Fatalf("want a tick error, got %v", err)
	}
	w.want(w.snapshot() == before, "a tick that cannot read main changes nothing")

	w.runner.Replies["git ls-tree --name-only origin/main -- .beads/plans/"] = verify.Reply{Out: ".beads/plans/p.jsonl\n"}
	w.runner.Replies["git fetch --quiet origin"] = verify.Reply{Err: errors.New("offline")}
	w.land("p-1")
	w.tick()
	w.want(strings.Contains(w.out.String(), "git fetch origin failed") && w.stage("p-2") == "spec",
		"a failed fetch reads the last fetched ref: p-2 %q", w.stage("p-2"))
}

// TestUnparkAfterRetryCouldNotStart: the retry's session could not be
// started (the way the observed bead parked before ADR-0009, as a launch
// collision). The stage that parked it is implement, so it resumes there.
func TestUnparkAfterRetryCouldNotStart(t *testing.T) {
	w := newWorld(t)
	w.failVerify()
	w.plan.Close(context.Background(), "p-1", "this PR")
	w.deck.StartErr = errors.New("tmux: no server")
	w.tick()
	w.want(w.stage("p-1") == "parked" && strings.Contains(w.out.String(), "remove stage:parked to resume implement"),
		"parked at implement: %q", w.stage("p-1"))
	w.deck.StartErr = nil
	w.plan.SetStage(context.Background(), "p-1", "")
	w.tick()
	w.want(w.stage("p-1") == "implement" && slices.Equal(w.deck.Sends, []string{"s2"}) && w.stage("p-2") == "",
		"resumes implement in s2, p-2 blocked: %q, sends %v, p-2 %q", w.stage("p-1"), w.deck.Sends, w.stage("p-2"))
	prompt := w.deck.Prompts[len(w.deck.Prompts)-1]
	w.want(strings.Contains(prompt, "--- FAIL: TestX"), "the resumed implement still gets the verify log:\n%s", prompt)
}
