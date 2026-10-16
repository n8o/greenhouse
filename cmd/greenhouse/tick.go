package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/n8o/greenhouse/internal/config"
	"github.com/n8o/greenhouse/internal/gate"
	"github.com/n8o/greenhouse/internal/launch"
	"github.com/n8o/greenhouse/internal/learncheck"
	"github.com/n8o/greenhouse/internal/ledger"
	"github.com/n8o/greenhouse/internal/plan"
	"github.com/n8o/greenhouse/internal/stage"
	"github.com/n8o/greenhouse/internal/verify"
)

// VerifyBase is the ref a PR is rebased onto and verified against.
const VerifyBase = "origin/main"

// adapters are every external effect a tick has: bd, agent-deck, gh, the
// usage and count probes, and the program runner verify and git use.
type adapters struct {
	plan   plan.Store
	deck   launch.Deck
	gh     GitHub
	probe  gate.Probe
	runner verify.Runner
	now    func() time.Time
}

// scheduler runs ticks (DESIGN.md "Pipeline"). It holds no state between
// ticks: each one rebuilds what it knows from bd, agent-deck, gh and
// .greenhouse/, so a scheduler killed at any point loses nothing.
//
// One tick:
//  1. collect: every finished stage session gets its outcome parsed,
//     appended to the ledger, and is stopped. A session past its stage's
//     wall-clock limit is stopped and recorded as failed.
//  2. advance: each bead takes the steps its state allows: carry out the
//     transition its last ledger record implies, launch its stage session
//     (gated), run verify, or read its PR's review outcome. At most one new
//     approved bead starts per tick.
//
// Idempotence: the ledger is the commit point. A transition is carried out
// while the bead's stage label still equals the stage of its last record,
// and the label moves last, so every effect before it is checked and
// repeated safely on the next tick after a crash. A launch is preceded by
// an open-run file, and a session already running for that run is adopted
// rather than launched again.
type scheduler struct {
	adapters
	cfg      config.Config
	launcher *launch.Launcher
	store    *ledger.Store
	local    *time.Location // zone of usage-limit reset times
	out      io.Writer
	dry      bool // print every decision, carry out none
}

// tick is what one Tick knows. In a dry run nothing is written, so tick
// also overlays the open runs the run would have written.
type tick struct {
	beads    []plan.Bead
	landed   plan.Landed // beads closed on VerifyBase: what "done" means (ADR-0010)
	recs     []ledger.Record
	kids     []launch.Child
	in       gate.Inputs
	runs     map[string]*ledger.Run // overlay: bead → open run (nil: none)
	started  bool                   // a new bead started this tick
	labelled bool                   // the greenhouse label is ensured
}

// Tick runs one pass.
func (s *scheduler) Tick(ctx context.Context) error {
	now := s.now()
	beads, err := s.plan.List(ctx)
	if err != nil {
		return err
	}
	landed, err := s.readLanded(ctx)
	if err != nil {
		return fmt.Errorf("read the plan on %s: %w", VerifyBase, err)
	}
	recs, err := s.store.Records()
	if err != nil {
		return err
	}
	kids, err := s.deck.Children(ctx, s.launcher.Parent)
	if err != nil {
		return fmt.Errorf("poll sessions: %w", err)
	}
	park, err := s.store.Park()
	if err != nil {
		return err
	}
	in := gate.Gather(ctx, s.probe, now)
	in.Quota, in.WIP = s.cfg.Quota, s.cfg.WIP
	in.Park = gate.Park{Until: park.Until, Reason: park.Reason}
	t := &tick{beads: beads, landed: landed, recs: recs, kids: kids, in: in, runs: map[string]*ledger.Run{}}

	fmt.Fprintf(s.out, "tick %s%s: %d beads, %d stage sessions, %d ledger records\n",
		now.Format(time.RFC3339), map[bool]string{true: " (dry run: nothing is carried out)"}[s.dry],
		len(beads), len(kids), len(recs))

	var errs []error
	for _, k := range kids {
		if err := s.collect(ctx, t, k); err != nil {
			errs = append(errs, fmt.Errorf("collect %s: %w", k.Title, err))
		}
	}
	for i := range t.beads {
		if err := s.advance(ctx, t, &t.beads[i]); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", t.beads[i].ID, err))
		}
	}
	if !t.started {
		fmt.Fprintln(s.out, "no new bead started: "+s.whyNoStart(t))
		for _, d := range gate.Evaluate(t.in, stage.Implement) {
			fmt.Fprintf(s.out, "  gate %s\n", d)
		}
	}
	return errors.Join(errs...)
}

// readLanded reads which beads are closed in the plan export at VerifyBase
// (ADR-0010). It fetches origin first; a failed fetch is reported and the
// ref is read as last fetched, so an offline tick sees fewer beads done,
// never more. A plan it cannot read fails the tick: read as "nothing is
// done", it would park every closed bead.
func (s *scheduler) readLanded(ctx context.Context) (plan.Landed, error) {
	git := func(args ...string) ([]byte, error) { return s.runner.Run(ctx, s.cfg.Repo, "git", args...) }
	if out, err := git("fetch", "--quiet", "origin"); err != nil {
		fmt.Fprintf(s.out, "git fetch origin failed (%v: %s); reading %s as last fetched\n",
			err, strings.TrimSpace(string(out)), VerifyBase)
	}
	out, err := git("ls-tree", "--name-only", VerifyBase, "--", plan.ExportDir)
	if err != nil {
		return nil, fmt.Errorf("git ls-tree: %w: %s", err, strings.TrimSpace(string(out)))
	}
	var files [][]byte
	for line := range strings.Lines(string(out)) {
		path := strings.TrimSpace(line)
		if !strings.HasSuffix(path, ".jsonl") {
			continue
		}
		data, err := git("show", VerifyBase+":"+path)
		if err != nil {
			return nil, fmt.Errorf("git show %s:%s: %w: %s", VerifyBase, path, err, strings.TrimSpace(string(data)))
		}
		files = append(files, data)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no plan under %s: the plan must be in git (ADR-0005)", plan.ExportDir)
	}
	return plan.ParseLanded(files...)
}

// say prints one decision line for bead.
func (s *scheduler) say(bead, format string, args ...any) {
	fmt.Fprintf(s.out, "%-10s %s\n", bead, fmt.Sprintf(format, args...))
}

// act prints an effect and carries it out unless this is a dry run.
func (s *scheduler) act(bead, what string, fn func() error) error {
	if s.dry {
		s.say(bead, "  would %s", what)
		return nil
	}
	s.say(bead, "  %s", what)
	return fn()
}

func (s *scheduler) run(t *tick, bead string) (*ledger.Run, error) {
	if r, ok := t.runs[bead]; ok {
		return r, nil
	}
	r, err := s.store.Run(bead)
	if err != nil {
		return nil, err
	}
	t.runs[bead] = r
	return r, nil
}

func (s *scheduler) putRun(t *tick, r ledger.Run, what string) error {
	t.runs[r.Bead] = &r
	return s.act(r.Bead, what, func() error { return s.store.PutRun(r) })
}

func (s *scheduler) deleteRun(t *tick, bead string) error {
	t.runs[bead] = nil
	return s.act(bead, "forget the open run", func() error { return s.store.DeleteRun(bead) })
}

func (s *scheduler) record(t *tick, r ledger.Record) error {
	t.recs = append(t.recs, r)
	what := fmt.Sprintf("append to ledger: %s %s", r.Stage, r.Outcome)
	if r.PR > 0 {
		what += fmt.Sprintf(" (PR #%d)", r.PR)
	}
	return s.act(r.Bead, what, func() error { return s.store.Append(r) })
}

func findBead(beads []plan.Bead, id string) *plan.Bead {
	for i := range beads {
		if beads[i].ID == id {
			return &beads[i]
		}
	}
	return nil
}

// collect handles one child session: it stops sessions already collected,
// and records the outcome of the bead's current run once it is finished or
// past its wall-clock limit.
func (s *scheduler) collect(ctx context.Context, t *tick, k launch.Child) error {
	id, st, ok := launch.ParseTitle(k.Title)
	b := findBead(t.beads, id)
	if !ok || b == nil {
		return nil // not a stage session of this plan
	}
	run, err := s.run(t, id)
	if err != nil {
		return err
	}
	ours := run != nil && run.Session == k.ID
	if ours && run.Reuse != nil && !run.Reuse.Sent {
		return nil // the retry prompt is not sent yet; the launch step sends it
	}
	collected := ledger.Has(t.recs, k.ID)
	if ours && run.Reuse != nil {
		// A reused session has records of its earlier runs; only this run's counts.
		collected = ledger.Count(t.recs, k.ID) > run.Reuse.PriorRuns
	}
	if collected {
		if ours {
			if err := s.deleteRun(t, id); err != nil {
				return err
			}
		}
		if k.Status != "stopped" {
			return s.act(id, "stop collected session "+k.ID, func() error { return s.deck.Stop(ctx, k.ID) })
		}
		return nil
	}
	if b.Stage() != string(st) || run == nil || run.Stage != string(st) || (run.Session != "" && run.Session != k.ID) {
		s.say(id, "session %s (%s, %s) is not the bead's open run; left alone for a human", k.ID, k.Title, k.Status)
		return nil
	}
	if run.Session == "" {
		adopted := *run
		adopted.Session = k.ID
		if err := s.putRun(t, adopted, "adopt session "+k.ID+" as the open "+string(st)+" run"); err != nil {
			return err
		}
		run = &adopted
	}

	now := s.now()
	rec := ledger.Record{Bead: id, Stage: string(st), Session: k.ID, Start: run.Start, End: now,
		Before: run.Before, After: quotaOf(t.in.Reading), Retries: run.Retries}
	ev, content, done, err := s.launcher.Collect(ctx, k.ID, st, priorOf(run))
	if !done {
		if err != nil {
			return err
		}
		limit := s.cfg.Timeout(st)
		if now.Sub(run.Start) <= limit {
			s.say(id, "[%s] session %s working (%s of %s)", st, k.ID, now.Sub(run.Start).Round(time.Second), limit)
			return nil
		}
		rec.TimedOut = true
		ev = timeoutEvent(st, limit)
	} else if p, hit := gate.ParseUsageLimit(content, now, s.local); hit {
		if err := s.act(id, fmt.Sprintf("park launches until %s: %s", p.Until.Format(time.RFC3339), p.Reason), func() error {
			return s.store.PutPark(ledger.Park{Until: p.Until, Reason: p.Reason})
		}); err != nil {
			return err
		}
		if p.Until.After(t.in.Park.Until) {
			t.in.Park = p
		}
		rec.Outcome, rec.Reason = ledger.UsageLimit, p.Reason
	} else if err != nil {
		ev = stage.Event{Kind: stage.NeedsHuman, Reason: fmt.Sprintf("session %s broke the result contract: %v", k.ID, err)}
	}
	if rec.Outcome == "" {
		rec.Outcome, rec.Reason, rec.PR = string(ev.Kind), ev.Reason, ev.PR
	}
	if st == stage.Learn && rec.Outcome == string(stage.LearnDone) {
		n, bad, err := s.checkLearn(ctx, id)
		if err != nil {
			return err // nothing is recorded yet; the next tick checks again
		}
		if bad != nil {
			rec.Outcome, rec.Reason = string(stage.NeedsHuman), "the learn session reported learn-done, but its PR fails the lesson check: "+bad.Error()
		} else {
			rec.PR = n
		}
	}
	s.say(id, "[%s] session %s finished: %s", st, k.ID, rec.Outcome)
	if err := s.record(t, rec); err != nil {
		return err
	}
	if err := s.deleteRun(t, id); err != nil {
		return err
	}
	return s.act(id, "stop session "+k.ID, func() error { return s.deck.Stop(ctx, k.ID) })
}

// checkLearn finds the learn session's PR by its branch and checks it is
// what learn may produce (learncheck.Check). It returns the PR number, why
// the PR fails the check (nil if it passes), and an error only when gh or
// git could not be read. It reads and never writes, so a tick may repeat it.
//
// The learn PR is opened by the session without the greenhouse label, so a
// lesson waiting on the human never counts toward wip.open_prs and never
// holds back implement launches.
func (s *scheduler) checkLearn(ctx context.Context, bead string) (int, error, error) {
	branch := launch.DefaultBranch(bead, stage.Learn)
	pr, err := s.gh.PRByBranch(ctx, branch)
	if err != nil {
		return 0, nil, err
	}
	if pr.Number == 0 {
		return 0, fmt.Errorf("no PR has head %s", branch), nil
	}
	git := func(args ...string) ([]byte, error) { return s.runner.Run(ctx, s.cfg.Repo, "git", args...) }
	if out, err := git("fetch", "--quiet", "origin"); err != nil {
		return 0, nil, fmt.Errorf("git fetch: %w: %s", err, out)
	}
	var files []learncheck.File
	for _, path := range pr.Files {
		f := learncheck.File{Path: path}
		// A lesson is added if the base lacks it and the head has it.
		if strings.HasPrefix(path, learncheck.LessonsDir) {
			if _, err := git("cat-file", "-e", VerifyBase+":"+path); err != nil {
				if out, err := git("show", "origin/"+branch+":"+path); err == nil {
					f.Added, f.Content = true, string(out)
				}
			}
		}
		files = append(files, f)
	}
	if err := learncheck.Check(files, pr.IsDraft, pr.State == "OPEN"); err != nil {
		return pr.Number, fmt.Errorf("PR #%d: %w", pr.Number, err), nil
	}
	return pr.Number, nil, nil
}

// priorOf is where run's reused session ended its previous attempt; the
// zero Prior for a run in a session of its own.
func priorOf(run *ledger.Run) launch.Prior {
	if run == nil || run.Reuse == nil {
		return launch.Prior{}
	}
	return launch.Prior{At: run.Reuse.PriorAt, Digest: run.Reuse.PriorDigest}
}

// timeoutEvent is how a session stopped at its wall-clock limit fails.
func timeoutEvent(st stage.Stage, limit time.Duration) stage.Event {
	why := fmt.Sprintf("%s session passed its %s wall-clock limit and was stopped", st, limit)
	if st == stage.Implement {
		return stage.Event{Kind: stage.ImplementFailed, Reason: why}
	}
	return stage.Event{Kind: stage.NeedsHuman, Reason: why + "; raise stages." + string(st) + ".timeout or split the bead"}
}

func quotaOf(r *gate.Reading) ledger.Quota {
	var q ledger.Quota
	if r == nil {
		return q
	}
	if r.FiveHour != nil {
		v := r.FiveHour.UsedPercentage
		q.FiveHour = &v
	}
	if r.SevenDay != nil {
		v := r.SevenDay.UsedPercentage
		q.SevenDay = &v
	}
	return q
}

// advance takes the steps bead's state allows. A dry run stops after the
// first, since the state it would have written is not there to read.
func (s *scheduler) advance(ctx context.Context, t *tick, b *plan.Bead) error {
	if st := b.Stage(); t.landed.Done(b.ID) && (st == "" || st == string(stage.Done)) {
		return nil
	}
	for range 8 {
		moved, err := s.step(ctx, t, b)
		if err != nil || !moved || s.dry {
			return err
		}
	}
	return nil
}

// step takes one step of bead, reporting whether it moved.
func (s *scheduler) step(ctx context.Context, t *tick, b *plan.Bead) (bool, error) {
	st := stage.Stage(b.Stage())
	run, err := s.run(t, b.ID)
	if err != nil {
		return false, err
	}
	if run != nil {
		switch {
		case st == "" && run.Stage == string(stage.Spec):
			// Crashed between writing the start's open run and its label.
			return true, s.setStage(ctx, b, stage.Spec)
		case run.Stage != string(st):
			s.say(b.ID, "open %s run does not match stage %q (changed by hand?); dropping it", run.Stage, st)
			return true, s.deleteRun(t, b.ID)
		case run.Session == "", run.Reuse != nil && !run.Reuse.Sent:
			return s.launch(ctx, t, b, st, run)
		case !slices.ContainsFunc(t.kids, func(k launch.Child) bool { return k.ID == run.Session }):
			// Removed outside greenhouse; its outcome is unknown.
			return true, s.vanished(t, b.ID, *run)
		}
		return false, nil // in flight; collect watches it
	}

	last, lastRun := ledger.Last(t.recs, b.ID), ledger.LastRun(t.recs, b.ID)
	if st == stage.Parked {
		s.say(b.ID, "[parked] waiting on a human (reason in the ledger); %s", unparkHint(lastRun))
		return false, nil
	}
	stillParked := slices.Contains(b.Labels, plan.StagePrefix+string(stage.Parked))
	switch {
	case last != nil && last.Outcome == ledger.Unparked && st == "":
		// The unpark is recorded; its label has not moved yet.
		return true, s.setStage(ctx, b, stage.Stage(last.Resume))
	case released(st, stillParked, last, lastRun):
		return s.unpark(ctx, t, b, st, *lastRun)
	case stillParked:
		// Released to st and recorded; drop the parked label.
		return true, s.setStage(ctx, b, st)
	case st == "" && lastRun == nil && b.Status == "closed":
		return s.unlanded(ctx, t, b)
	}
	if st != "" && last != nil && last.Stage == string(st) && last.Outcome != ledger.UsageLimit {
		return s.apply(ctx, t, b, st, *last)
	}
	switch st {
	case "":
		return s.start(ctx, t, b)
	case stage.Spec, stage.Implement, stage.Learn:
		return s.launch(ctx, t, b, st, nil)
	case stage.Verify:
		return s.verify(ctx, t, b)
	case stage.Review:
		return s.review(ctx, t, b)
	}
	if st == stage.Done && !t.landed.Done(b.ID) {
		s.say(b.ID, "[done] merged, but its close is not on %s yet; its dependents wait", VerifyBase)
	}
	return false, nil
}

// eventOf is the stage event rec recorded.
func eventOf(rec ledger.Record) stage.Event {
	return stage.Event{Kind: stage.EventKind(rec.Outcome), Reason: rec.Reason, PR: rec.PR, Retries: rec.Retries}
}

// parks reports whether rec is a stage run whose outcome parked its bead.
func parks(rec ledger.Record) bool {
	if !rec.IsRun() || rec.Outcome == ledger.UsageLimit {
		return false
	}
	next, _, err := stage.Next(stage.Stage(rec.Stage), eventOf(rec))
	return err == nil && next == stage.Parked
}

// resumeOf is the stage a bead whose last stage run is rec resumes in when
// it is released without a stage of the human's choosing (ADR-0010).
func resumeOf(rec ledger.Record) (stage.Stage, error) {
	if rec.Outcome == ledger.UsageLimit {
		return stage.Stage(rec.Stage), nil
	}
	return stage.Resume(stage.Stage(rec.Stage), eventOf(rec))
}

// released reports whether a human has released a bead with stage label st
// whose last record is last and last stage run lastRun (ADR-0010). It has
// runs, and either no stage label (stage:parked was removed, or the label
// was lost), or a stage label added next to stage:parked (stillParked), or
// a run parked it and its label is now neither stage:parked nor that run's
// stage. A label equal to the parking run's stage without stage:parked
// beside it is an unfinished transition (ADR-0007), not a release. An
// unpark already recorded is not a new release.
func released(st stage.Stage, stillParked bool, last, lastRun *ledger.Record) bool {
	if lastRun == nil || !last.IsRun() {
		return false
	}
	return st == "" || stillParked || st != stage.Stage(lastRun.Stage) && parks(*lastRun)
}

// unpark resumes a released bead: in st if the human chose it, else in the
// stage its last run implies (the stage that parked it). The unpark is a
// ledger record, which resets the verify retry budget, and the label moves
// last (ADR-0007).
func (s *scheduler) unpark(ctx context.Context, t *tick, b *plan.Bead, st stage.Stage, lastRun ledger.Record) (bool, error) {
	if b.Status == "closed" {
		s.say(b.ID, "closed in the local bd database, but its close is not on %s: not done; it resumes as an in-flight bead", VerifyBase)
	}
	resume := st
	why := "a human set stage:" + string(st)
	if st == "" {
		r, err := resumeOf(lastRun)
		if err != nil {
			return s.parkBead(ctx, t, b, fmt.Sprintf("released with no stage label, but its last run (%s, %s) says nothing to resume: %v. "+
				"Add the stage label it should resume in (stage:spec restarts it).", lastRun.Stage, lastRun.Outcome, err))
		}
		resume = r
		why = fmt.Sprintf("no stage label; its last run was %s, %s", lastRun.Stage, lastRun.Outcome)
	}
	pr := ledger.LastPR(t.recs, b.ID)
	next, _, err := stage.Next(stage.Parked, stage.Event{Kind: stage.Unparked, Resume: resume, PR: pr})
	if err != nil {
		s.say(b.ID, "[%s] released, but it cannot resume there: %v; left for a human", resume, err)
		return false, nil
	}
	s.say(b.ID, "[parked] released (%s) → resume %s, verify retries reset", why, next)
	now := s.now()
	q := quotaOf(t.in.Reading)
	if err := s.record(t, ledger.Record{Bead: b.ID, Stage: ledger.ParkedStage, Start: now, End: now, Before: q, After: q,
		Outcome: ledger.Unparked, Resume: string(next), Reason: why, PR: pr}); err != nil {
		return true, err
	}
	return true, s.setStage(ctx, b, next)
}

// unlanded handles a bead closed in the local bd database, with no stage
// label and no run in the ledger, whose close is not on VerifyBase. It is
// neither done nor in flight, so an approved bead is parked with the reason
// rather than guessed at; any other bead is only reported.
func (s *scheduler) unlanded(ctx context.Context, t *tick, b *plan.Bead) (bool, error) {
	msg := fmt.Sprintf("closed in the local bd database, but its close is not on %s and greenhouse has no run of it", VerifyBase)
	if !b.Approved() {
		s.say(b.ID, "%s: not done; its dependents wait for its PR to merge", msg)
		return false, nil
	}
	return s.parkBead(ctx, t, b, msg+". Merge the PR that closes it, or reopen it (bd reopen "+b.ID+") and remove stage:parked to run it.")
}

// parkBead parks b for a human without a stage run: the reason is a ledger
// record, and the label moves last.
func (s *scheduler) parkBead(ctx context.Context, t *tick, b *plan.Bead, reason string) (bool, error) {
	s.say(b.ID, "park: %s", reason)
	now := s.now()
	q := quotaOf(t.in.Reading)
	if err := s.record(t, ledger.Record{Bead: b.ID, Stage: ledger.ParkedStage, Start: now, End: now, Before: q, After: q,
		Outcome: string(stage.NeedsHuman), Reason: reason}); err != nil {
		return true, err
	}
	return true, s.setStage(ctx, b, stage.Parked)
}

// unparkHint tells the human how to release a parked bead whose last stage
// run is lastRun.
func unparkHint(lastRun *ledger.Record) string {
	const restart = "add stage:spec to restart at spec"
	if lastRun == nil {
		return "remove stage:parked to start it (reopen it first if it is closed)"
	}
	r, err := resumeOf(*lastRun)
	if err != nil {
		return "add the stage label it should resume in; " + restart
	}
	if r == stage.Spec {
		return "remove stage:parked to resume spec"
	}
	return fmt.Sprintf("remove stage:parked to resume %s, or %s", r, restart)
}

func (s *scheduler) setStage(ctx context.Context, b *plan.Bead, st stage.Stage) error {
	err := s.act(b.ID, "label stage:"+string(st), func() error { return s.plan.SetStage(ctx, b.ID, string(st)) })
	if err == nil && !s.dry {
		var keep []string
		for _, l := range b.Labels {
			if !strings.HasPrefix(l, plan.StagePrefix) {
				keep = append(keep, l)
			}
		}
		b.Labels = append(keep, plan.StagePrefix+string(st))
	}
	return err
}

// apply carries out the transition last implies. Every effect is checked
// first or safe to repeat; the label moves last.
func (s *scheduler) apply(ctx context.Context, t *tick, b *plan.Bead, st stage.Stage, last ledger.Record) (bool, error) {
	ev := eventOf(last)
	next, acts, err := stage.Next(st, ev)
	if err != nil {
		s.say(b.ID, "[%s] cannot route %s: %v; left for a human", st, ev.Kind, err)
		return false, nil
	}
	kinds := make([]string, len(acts))
	for i, a := range acts {
		kinds[i] = string(a.Kind)
	}
	s.say(b.ID, "[%s] event %s → %s; actions [%s]", st, ev.Kind, next, strings.Join(kinds, " "))
	for _, a := range acts {
		var err error
		switch a.Kind {
		case stage.ActVerify:
			err = s.labelPR(ctx, t, b.ID, a.PR)
		case stage.ActMarkReady:
			err = s.markReady(ctx, b.ID, a.PR)
		case stage.ActCloseBead:
			if b.Status == "closed" {
				s.say(b.ID, "  the bead is already closed (by its PR)")
				break
			}
			err = s.act(b.ID, "close the bead", func() error {
				return s.plan.Close(ctx, b.ID, fmt.Sprintf("merged in PR #%d", last.PR))
			})
		case stage.ActPark:
			s.say(b.ID, "  park: %s", firstLine(a.Reason))
		case stage.ActLaunch, stage.ActRecord:
			// The launch is the next step (gated); the record is the ledger line.
		}
		if err != nil {
			return false, err
		}
	}
	if next == stage.Done {
		b.Status = "closed"
	}
	return true, s.setStage(ctx, b, next)
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

// labelPR ensures the greenhouse label exists and is on PR n.
func (s *scheduler) labelPR(ctx context.Context, t *tick, bead string, n int) error {
	if !t.labelled {
		if err := s.act(bead, "ensure the "+gate.PRLabel+" label exists", func() error { return s.gh.EnsureLabel(ctx) }); err != nil {
			return err
		}
		t.labelled = true
	}
	return s.act(bead, fmt.Sprintf("label PR #%d %s", n, gate.PRLabel), func() error { return s.gh.AddLabel(ctx, n) })
}

func (s *scheduler) markReady(ctx context.Context, bead string, n int) error {
	pr, err := s.gh.PR(ctx, n)
	if err != nil {
		return err
	}
	if !pr.IsDraft {
		s.say(bead, "  PR #%d is already ready for review", n)
		return nil
	}
	return s.act(bead, fmt.Sprintf("mark PR #%d ready for review", n), func() error { return s.gh.Ready(ctx, n) })
}

// start moves a ready approved bead with no stage into spec, if the gates
// allow and no other bead started this tick.
func (s *scheduler) start(ctx context.Context, t *tick, b *plan.Bead) (bool, error) {
	if !b.Approved() || t.started {
		return false, nil
	}
	if !plan.Ready(*b, t.landed) {
		s.say(b.ID, "approved, not ready: %s", blockers(*b, t))
		return false, nil
	}
	next, _ := stage.Start()
	if !s.gates(t, b.ID, next) {
		return false, nil
	}
	t.started = true
	s.say(b.ID, "start: ready and %s → %s", plan.ApprovedLabel, next)
	if err := s.putRun(t, s.newRun(t, b.ID, next), "open a "+string(next)+" run"); err != nil {
		return false, err
	}
	return true, s.setStage(ctx, b, next)
}

// blockers describes why b is not ready.
func blockers(b plan.Bead, t *tick) string {
	if b.Status != "open" {
		return "status " + b.Status
	}
	var open []string
	for _, id := range plan.Blockers(b) {
		if t.landed.Done(id) {
			continue
		}
		if x := findBead(t.beads, id); x != nil && x.Status == "closed" {
			id += " (closed locally, not on " + VerifyBase + ")"
		}
		open = append(open, id)
	}
	return "waits on " + strings.Join(open, ", ")
}

func (s *scheduler) whyNoStart(t *tick) string {
	for _, b := range t.beads {
		if b.Stage() == "" && b.Approved() && plan.Ready(b, t.landed) {
			return "gates hold " + b.ID
		}
	}
	return "no open " + plan.ApprovedLabel + " bead without a stage has all its blockers closed on " + VerifyBase
}

func (s *scheduler) newRun(t *tick, bead string, st stage.Stage) ledger.Run {
	return ledger.Run{Bead: bead, Stage: string(st), Start: s.now(), Before: quotaOf(t.in.Reading),
		Retries: ledger.Retries(t.recs, bead)}
}

// gates evaluates and prints the launch gates for an st session.
func (s *scheduler) gates(t *tick, bead string, st stage.Stage) bool {
	ds := gate.Evaluate(t.in, st)
	for _, d := range ds {
		s.say(bead, "  gate %s", d)
	}
	ok, _ := gate.Allowed(ds)
	return ok
}

// launch starts bead's st session, if the gates allow. run is the open run
// to launch, or nil to open one.
func (s *scheduler) launch(ctx context.Context, t *tick, b *plan.Bead, st stage.Stage, run *ledger.Run) (bool, error) {
	s.say(b.ID, "[%s] launch a %s session", st, st)
	if !s.gates(t, b.ID, st) {
		s.say(b.ID, "  held by the gates")
		return false, nil
	}
	if run == nil {
		r := s.newRun(t, b.ID, st)
		if err := s.putRun(t, r, "open a "+string(st)+" run"); err != nil {
			return false, err
		}
		run = &r
	}
	req := launch.Request{Bead: b.ID, Title: b.Title, DoneWhen: b.Description, Spec: specOf(*b),
		Reason: launchReason(t.recs, b.ID, st), Stage: st, Branch: launch.DefaultBranch(b.ID, st)}
	// fail records a launch that could not happen as needing a human.
	fail := func(why string) (bool, error) {
		rec := ledger.Record{Bead: b.ID, Stage: string(st), Start: run.Start, End: s.now(), Before: run.Before,
			After: quotaOf(t.in.Reading), Retries: run.Retries, Outcome: string(stage.NeedsHuman), Reason: why}
		if err := s.record(t, rec); err != nil {
			return true, err
		}
		return true, s.deleteRun(t, b.ID)
	}
	if st == stage.Learn {
		// Learn studies the rejected PR: the review record's.
		req.PR = ledger.LastPR(t.recs, b.ID)
		if req.PR > 0 {
			pr, err := s.gh.PR(ctx, req.PR)
			if err != nil {
				return fail(fmt.Sprintf("could not read the rejected PR #%d for the learn session: %v", req.PR, err))
			}
			req.HeadRef = pr.HeadRef
		}
	}

	// A session titled for this stage still exists: its title is taken, and
	// its context and worktree are worth keeping, so the retry continues it.
	ex, found, err := s.launcher.Find(ctx, b.ID, st)
	if err != nil {
		return false, err
	}
	if found {
		return s.resume(ctx, t, b, run, req, ex)
	}
	if run.Reuse != nil {
		s.say(b.ID, "  session %s chosen for the retry is gone; launching a new one", run.Session)
		fresh := *run
		fresh.Session, fresh.Reuse = "", nil
		if err := s.putRun(t, fresh, "clear the reused session from the open run"); err != nil {
			return false, err
		}
		run = &fresh
	}

	req.ExistingBranch = s.branchExists(ctx, req.Branch)
	if s.dry {
		s.say(b.ID, "  would launch %s on %s (existing branch: %v)", launch.Title(b.ID, st), req.Branch, req.ExistingBranch)
		return true, nil
	}
	s.say(b.ID, "  launch %s on %s", launch.Title(b.ID, st), req.Branch)
	r, err := s.launcher.Launch(ctx, req)
	if r.ID != "" {
		started := *run
		started.Session = r.ID
		if perr := s.putRun(t, started, "record session "+r.ID+" on the open run"); perr != nil {
			return true, errors.Join(err, perr)
		}
		t.kids = append(t.kids, launch.Child{ID: r.ID, Title: launch.Title(b.ID, st), Status: "starting"})
		if st == stage.Implement {
			t.in.Implement++
		}
	}
	if err == nil {
		return true, nil
	}
	if r.ID != "" {
		// The session exists; collect judges it (an undelivered prompt
		// leaves it idle with no reply, which parks the bead).
		s.say(b.ID, "  launch problem: %v", err)
		return true, nil
	}
	return fail(fmt.Sprintf("could not launch the %s session: %v", st, err))
}

// resume continues ex, the existing session of b's stage, as the open run:
// the run records the session and where its previous attempt ended before
// anything is sent, then the launcher restarts the session if it is stopped
// and sends it the retry prompt (ADR-0009).
func (s *scheduler) resume(ctx context.Context, t *tick, b *plan.Bead, run *ledger.Run, req launch.Request, ex launch.Existing) (bool, error) {
	st := req.Stage
	req.Reason = retryReason(t.recs, b.ID, st, req.Reason)
	if run.Reuse == nil || run.Session != ex.ID {
		r := *run
		r.Session = ex.ID
		r.Reuse = &ledger.Reuse{PriorAt: ex.Prior.At, PriorDigest: ex.Prior.Digest,
			PriorRuns: ledger.Count(t.recs, ex.ID)}
		if err := s.putRun(t, r, fmt.Sprintf("reuse session %s (%s) for the open %s run", ex.ID, ex.Status, st)); err != nil {
			return false, err
		}
		run = &r
	} else {
		// A tick crashed between choosing the session and recording the
		// send: keep the baseline taken before anything was sent.
		ex.Prior = priorOf(run)
	}
	if s.dry {
		s.say(b.ID, "  would resume %s (%s): start it if stopped, then send the retry prompt", ex.Title, ex.Status)
		return true, nil
	}
	s.say(b.ID, "  resume %s (%s) on %s", ex.Title, ex.Status, req.Branch)
	r, err := s.launcher.Resume(ctx, req, ex)
	if r.Sent || r.Adopted {
		sent := *run
		reuse := *run.Reuse
		reuse.Sent = true
		sent.Reuse = &reuse
		what := "record the retry sent to " + ex.ID
		switch {
		case r.Adopted:
			what = "adopt " + ex.ID + ", already working, as the retry"
		case r.Restarted:
			what = "record the retry sent to " + ex.ID + " (started again)"
		}
		if perr := s.putRun(t, sent, what); perr != nil {
			return true, errors.Join(err, perr)
		}
		if i := slices.IndexFunc(t.kids, func(k launch.Child) bool { return k.ID == ex.ID }); i >= 0 {
			t.kids[i].Status = "starting"
		} else {
			t.kids = append(t.kids, launch.Child{ID: ex.ID, Title: ex.Title, Status: "starting"})
		}
		if st == stage.Implement {
			t.in.Implement++
		}
	}
	if err == nil {
		return true, nil
	}
	if r.Sent {
		// The prompt is in the session; collect judges its reply.
		s.say(b.ID, "  resume problem: %v", err)
		return true, nil
	}
	rec := ledger.Record{Bead: b.ID, Stage: string(st), Session: ex.ID, Start: run.Start, End: s.now(),
		Before: run.Before, After: quotaOf(t.in.Reading), Retries: run.Retries,
		Outcome: string(stage.NeedsHuman), Reason: fmt.Sprintf("could not resume the %s session %s: %v", st, ex.ID, err)}
	if err := s.record(t, rec); err != nil {
		return true, err
	}
	return true, s.deleteRun(t, b.ID)
}

// retryReason tells a resumed stage session why it runs again. reason is
// the launch reason (the verify log of an implement retry), if any.
func retryReason(recs []ledger.Record, bead string, st stage.Stage, reason string) string {
	why := retryWhy(recs, bead, st, reason)
	if last := ledger.Last(recs, bead); last != nil && last.Outcome == ledger.Unparked {
		return fmt.Sprintf("A human looked at this bead while it was parked and released it to the %s stage, "+
			"so the question or failure that parked it may be answered in the bead now.\n\n%s", st, why)
	}
	return why
}

func retryWhy(recs []ledger.Record, bead string, st stage.Stage, reason string) string {
	if reason != "" && st == stage.Implement {
		return "Verification of your draft PR failed. Its log:\n\n" + reason
	}
	if reason != "" {
		return reason
	}
	last := ledger.LastRun(recs, bead)
	switch {
	case last == nil:
		return fmt.Sprintf("greenhouse is running the %s stage again.", st)
	case last.Stage == string(st) && last.Outcome == ledger.UsageLimit:
		return "A Claude usage limit stopped your previous turn before it finished. The limit has reset: continue where you left off."
	}
	why := fmt.Sprintf("The bead went back through the pipeline: its last run was %s, %s", last.Stage, last.Outcome)
	if r := firstLine(last.Reason); r != "" {
		why += ": " + r
	}
	return why + fmt.Sprintf(". So the %s stage runs again.", st)
}

// vanished records an open run whose session no longer exists as failed,
// for a human to look at.
func (s *scheduler) vanished(t *tick, bead string, run ledger.Run) error {
	s.say(bead, "[%s] session %s is gone without a collected result", run.Stage, run.Session)
	rec := ledger.Record{Bead: bead, Stage: run.Stage, Session: run.Session, Start: run.Start, End: s.now(),
		Before: run.Before, After: quotaOf(t.in.Reading), Retries: run.Retries,
		Outcome: string(stage.NeedsHuman), Reason: fmt.Sprintf("%s session %s disappeared before greenhouse collected its result", run.Stage, run.Session)}
	if err := s.record(t, rec); err != nil {
		return err
	}
	return s.deleteRun(t, bead)
}

func specOf(b plan.Bead) string {
	var parts []string
	if b.Acceptance != "" {
		parts = append(parts, "Acceptance criteria:\n"+b.Acceptance)
	}
	if b.Design != "" {
		parts = append(parts, "File plan:\n"+b.Design)
	}
	return strings.Join(parts, "\n\n")
}

// launchReason is the scheduler's context for an st session: the verify log
// for an implement retry, the rejection reason for learn. An implement retry
// that never finished (its session could not start, or it asked a question
// and was unparked) still gets the verify log that sent it back.
func launchReason(recs []ledger.Record, bead string, st stage.Stage) string {
	for i := len(recs) - 1; i >= 0; i-- {
		r := recs[i]
		switch {
		case r.Bead != bead || !r.IsRun():
		case st == stage.Implement && r.Outcome == string(stage.VerifyFail),
			st == stage.Learn && r.Outcome == string(stage.Rejected):
			return r.Reason
		case st == stage.Implement && r.Stage == string(stage.Implement) &&
			r.Outcome != string(stage.ImplementDone) && r.Outcome != ledger.UsageLimit:
			// An implement retry that parked unfinished: look past it. (A
			// usage-limit stop is told to continue where it left off.)
		default:
			return ""
		}
	}
	return ""
}

func (s *scheduler) branchExists(ctx context.Context, branch string) bool {
	_, err := s.runner.Run(ctx, s.cfg.Repo, "git", "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

// verify runs the deterministic checks on bead's PR, rebased onto
// VerifyBase in a scheduler-owned worktree, and records the verdict.
func (s *scheduler) verify(ctx context.Context, t *tick, b *plan.Bead) (bool, error) {
	n := ledger.LastPR(t.recs, b.ID)
	if n == 0 {
		s.say(b.ID, "[verify] no PR recorded for the bead; left for a human")
		return false, nil
	}
	pr, err := s.gh.PR(ctx, n)
	if err != nil {
		return false, err
	}
	if pr.State != "OPEN" {
		s.say(b.ID, "[verify] PR #%d is %s, not open; left for a human", n, pr.State)
		return false, nil
	}
	retries := ledger.Retries(t.recs, b.ID)
	if s.dry {
		s.say(b.ID, "[verify] would verify PR #%d (%s rebased onto %s), %d/%d retries used",
			n, pr.HeadRef, VerifyBase, retries, stage.MaxVerifyRetries)
		return true, nil
	}
	start := s.now()
	s.say(b.ID, "[verify] PR #%d (%s rebased onto %s)", n, pr.HeadRef, VerifyBase)
	res, err := s.runVerify(ctx, b.ID, pr.HeadRef)
	if err != nil {
		return false, err
	}
	verify.Print(prefixWriter{s.out, "             "}, res)
	ev := res.Event(n, retries)
	return true, s.record(t, ledger.Record{Bead: b.ID, Stage: string(stage.Verify), Start: start, End: s.now(),
		Before: quotaOf(t.in.Reading), After: quotaOf(t.in.Reading), Retries: retries,
		Outcome: string(ev.Kind), Reason: ev.Reason, PR: n})
}

// runVerify checks out head in .greenhouse/verify/<bead>, rebases it onto
// VerifyBase and runs verify there. A rebase conflict is a verify failure:
// the PR cannot merge as it is.
func (s *scheduler) runVerify(ctx context.Context, bead, head string) (verify.Result, error) {
	dir := filepath.Join(s.store.Dir, "verify", bead)
	git := func(dir string, args ...string) ([]byte, error) { return s.runner.Run(ctx, dir, "git", args...) }
	if out, err := git(s.cfg.Repo, "fetch", "--quiet", "origin"); err != nil {
		return verify.Result{}, fmt.Errorf("git fetch: %w: %s", err, out)
	}
	if _, err := os.Stat(dir); err != nil {
		if out, err := git(s.cfg.Repo, "worktree", "add", "--force", "--detach", dir, "origin/"+head); err != nil {
			return verify.Result{}, fmt.Errorf("git worktree add: %w: %s", err, out)
		}
	} else if out, err := git(dir, "checkout", "--force", "--detach", "origin/"+head); err != nil {
		return verify.Result{}, fmt.Errorf("git checkout: %w: %s", err, out)
	}
	if out, err := git(dir, "-c", "user.name=greenhouse", "-c", "user.email=greenhouse@invalid",
		"rebase", VerifyBase); err != nil {
		git(dir, "rebase", "--abort")
		log := fmt.Sprintf("rebase onto %s failed: %v\n%s\nRebase the branch onto %s, resolve the conflicts and push.\n",
			VerifyBase, err, strings.TrimSpace(string(out)), VerifyBase)
		return verify.Result{Checks: []verify.Check{{Name: "rebase " + VerifyBase, Detail: err.Error()}}, Log: log}, nil
	}
	return verify.Run(ctx, s.runner, verify.Options{Dir: dir, Base: VerifyBase, Bead: bead,
		Commands: s.cfg.Verify.Commands, Timeout: s.cfg.Verify.CommandTimeout()})
}

// review reads the human's verdict on bead's ready PR.
func (s *scheduler) review(ctx context.Context, t *tick, b *plan.Bead) (bool, error) {
	n := ledger.LastPR(t.recs, b.ID)
	if n == 0 {
		s.say(b.ID, "[review] no PR recorded for the bead; left for a human")
		return false, nil
	}
	pr, err := s.gh.PR(ctx, n)
	if err != nil {
		return false, err
	}
	rec := ledger.Record{Bead: b.ID, Stage: string(stage.Review), Start: s.now(), End: s.now(),
		Before: quotaOf(t.in.Reading), After: quotaOf(t.in.Reading), PR: n}
	switch pr.State {
	case "MERGED":
		rec.Outcome = string(stage.Merged)
	case "CLOSED":
		rec.Outcome = string(stage.Rejected)
		rec.Reason = pr.Reason
		if rec.Reason == "" {
			rec.Reason = fmt.Sprintf("PR #%d was closed without merging and without a review comment", n)
		}
	default:
		s.say(b.ID, "[review] PR #%d is %s; waiting on the human", n, strings.ToLower(pr.State))
		return false, nil
	}
	if last := ledger.Last(t.recs, b.ID); last != nil {
		rec.Start = last.End
	}
	s.say(b.ID, "[review] PR #%d %s", n, strings.ToLower(pr.State))
	return true, s.record(t, rec)
}

// prefixWriter indents every line written through it.
type prefixWriter struct {
	w      io.Writer
	prefix string
}

func (p prefixWriter) Write(b []byte) (int, error) {
	for line := range strings.Lines(string(b)) {
		if _, err := io.WriteString(p.w, p.prefix+line); err != nil {
			return 0, err
		}
	}
	return len(b), nil
}
