// Package report rolls the ledger up into the numbers that say whether the
// factory works (docs/DESIGN.md "Ledger"): quota per merged PR, cycle time,
// parks, retries and rejections, and whether scoped memory (nugit context
// calls) is used and helps.
//
// It is pure: Build takes the ledger records, the nugit usage lines and the
// first-commit times already read by the caller, and does no I/O and reads no
// clock. Its only imports are ledger and stage.
//
// The JSON form of Report is a stable schema: snake_case keys, arrays never
// null, durations in whole seconds, quota in percentage points.
package report

import (
	"cmp"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/n8o/greenhouse/internal/ledger"
	"github.com/n8o/greenhouse/internal/stage"
)

// DefaultMinSample is how many beads each comparison group needs before
// Build compares their outcomes.
const DefaultMinSample = 5

// maxReason is how long a reason may be once grouped: a verify log or a
// rejection note is grouped by its first line, cut to this many bytes.
const maxReason = 120

// Use is one line of the nugit usage log, as Build needs it.
type Use struct {
	Time   time.Time
	Source string // cli | mcp | hook; hook lines are edits, not context calls
	Branch string
	// AgentSession is the agent-deck instance id of the caller, which is the
	// ledger's Session of a stage run; "" when the caller did not export it.
	AgentSession string
	Component    string // "" = the path resolved to no component
	Items        int    // decisions+lessons+references+glossary+working_memory+path_history
	Spec         bool
}

// Context reports whether u is a context call rather than a hook edit.
func (u Use) Context() bool { return u.Source == "cli" || u.Source == "mcp" }

// Commit is the first commit a stage run made on its branch. A run missing
// from Options.Commits has an unknown first commit.
type Commit struct {
	At   time.Time
	None bool // the branch has no commit since the run started
}

// Options is what Build needs besides the records and usage lines.
type Options struct {
	// LLMStages are the stages whose runs are Claude sessions; nil means any
	// record with a Session.
	LLMStages []stage.Stage
	// Branch names the branch a stage run of bead works on; nil disables the
	// branch join.
	Branch func(bead string, st stage.Stage) string
	// Commits holds the first commit of each stage run, by ledger Session.
	Commits map[string]Commit
	// MinSample is how many beads each comparison group needs; <= 0 means
	// DefaultMinSample.
	MinSample int
}

// Report is the rolled-up ledger.
type Report struct {
	Runs       int        `json:"runs"`
	Beads      int        `json:"beads"`
	Quota      Quota      `json:"quota"`
	Cycle      Cycle      `json:"cycle"`
	Parks      Parks      `json:"parks"`
	Retries    Retries    `json:"retries"`
	Rejects    Rejects    `json:"rejects"`
	Memory     Memory     `json:"memory"`
	Comparison Comparison `json:"comparison"`
}

// Quota is the usage each merged PR cost, summed over every run of its bead.
type Quota struct {
	Merged       []MergedPR `json:"merged"`
	MeanFiveHour float64    `json:"mean_five_hour"` // over Merged; 0 when empty
	MeanSevenDay float64    `json:"mean_seven_day"`
}

// MergedPR is one merged bead's quota cost. A run whose before or after
// reading lacks either window is left out of the sums and counted in Unknown.
type MergedPR struct {
	PR       int     `json:"pr"`
	Bead     string  `json:"bead"`
	FiveHour float64 `json:"five_hour"` // percentage points of the 5-hour window
	SevenDay float64 `json:"seven_day"`
	Unknown  int     `json:"unknown_runs"`
}

// Cycle is how long each bead took from its first run to its PR's merge or
// rejection.
type Cycle struct {
	Beads []BeadCycle `json:"beads"`
	// MedianSeconds is over finished (merged or rejected) beads only; 0 when
	// none has finished.
	MedianSeconds int64 `json:"median_seconds"`
	Finished      int   `json:"finished"`
}

// BeadCycle is one bead's cycle time. An open bead's runs from its first
// start to its last end.
type BeadCycle struct {
	Bead    string `json:"bead"`
	State   string `json:"state"` // merged | rejected | open
	Seconds int64  `json:"seconds"`
}

// Parks are the beads that stopped for a human.
type Parks struct {
	Parked int     `json:"parked"` // beads with at least one parking run
	Beads  int     `json:"beads"`  // beads with any record
	Rate   float64 `json:"rate"`   // Parked / Beads; 0 when Beads is 0
	// Reasons groups the parking runs by reason, most frequent first.
	Reasons []Reason `json:"reasons"`
	// UsageLimits counts runs stopped by a usage limit. They are relaunched
	// once the park lifts, so they are not bead parks.
	UsageLimits int `json:"usage_limits"`
}

// Reason is a reason text and how many runs gave it.
type Reason struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

// Retries are the verify failures that sent beads back to implement.
type Retries struct {
	Beads       []BeadRetries `json:"beads"` // beads with at least one verify run
	VerifyFails int           `json:"verify_fails"`
	// Distribution counts beads by their number of verify failures.
	Distribution []Bucket `json:"distribution"`
	TimedOut     int      `json:"timed_out"` // runs stopped at their wall-clock limit
}

// BeadRetries is one bead's retry history.
type BeadRetries struct {
	Bead        string `json:"bead"`
	MaxRetries  int    `json:"max_retries"` // the highest Retries any of its runs started with
	VerifyFails int    `json:"verify_fails"`
}

// Bucket is how many beads had VerifyFails verify failures.
type Bucket struct {
	VerifyFails int `json:"verify_fails"`
	Beads       int `json:"beads"`
}

// Rejects are the PRs the human closed unmerged.
type Rejects struct {
	Count   int      `json:"count"`
	Reasons []Reason `json:"reasons"`
}

// Memory is how stage runs used scoped memory (nugit context calls).
type Memory struct {
	Runs   []Run       `json:"runs"`
	Totals MemoryTotal `json:"totals"`
}

// Run is one LLM stage run joined to the usage lines it made.
type Run struct {
	Bead    string `json:"bead"`
	Stage   string `json:"stage"`
	Session string `json:"session"`
	// Join is how its lines were found: "session" (agent_session equals the
	// ledger session), "branch" (its branch, within its start and end), or
	// "none" when no line matched.
	Join         string `json:"join"`
	ContextCalls int    `json:"context_calls"`
	// BeforeCommit counts the calls made before the run's first commit; all
	// of them when it made none. Omitted when the first commit is unknown.
	BeforeCommit *int `json:"before_first_commit,omitempty"`
	EmptyBundles int  `json:"empty_bundles"`    // no items and no spec
	Unresolved   int  `json:"unresolved_paths"` // the path mapped to no component
}

// MemoryTotal sums Memory.Runs.
type MemoryTotal struct {
	Runs            int `json:"runs"`
	RunsWithContext int `json:"runs_with_context"`
	ContextCalls    int `json:"context_calls"`
	BeforeCommit    int `json:"before_first_commit"` // over runs whose first commit is known
	CommitUnknown   int `json:"commit_unknown_runs"`
	EmptyBundles    int `json:"empty_bundles"`
	Unresolved      int `json:"unresolved_paths"`
	BySession       int `json:"joined_by_session"` // runs
	ByBranch        int `json:"joined_by_branch"`
	// Unattributed counts context calls no stage run matched: interactive
	// sessions, or runs the ledger does not know.
	Unattributed int `json:"unattributed_calls"`
}

// Comparison splits beads into those whose LLM runs loaded context and those
// whose did not, and compares their outcomes. The unit is the bead, because
// every outcome compared (parks, merges, retries, review rounds) is a bead's;
// a bead is "loaded" when any of its LLM stage runs made at least one context
// call. Beads with no LLM run are in neither group.
type Comparison struct {
	MinSample int `json:"min_sample"`
	// Comparable is false while either group has fewer than MinSample beads;
	// the groups then carry their counts only.
	Comparable bool  `json:"comparable"`
	Loaded     Group `json:"loaded"`
	None       Group `json:"none"`
}

// Group is one side of the comparison. Outcomes is nil, and omitted, unless
// the comparison is Comparable: too few beads say nothing.
type Group struct {
	N        int       `json:"n"`
	Outcomes *Outcomes `json:"outcomes,omitempty"`
}

// Outcomes are a group's bead outcomes.
type Outcomes struct {
	// VerifyRetries is the mean number of verify failures per bead.
	VerifyRetries float64 `json:"verify_retries"`
	ParkRate      float64 `json:"park_rate"`
	Merged        int     `json:"merged"`
	Rejected      int     `json:"rejected"`
	// ReviewRounds is the mean number of review rounds per bead. A round is
	// one verify-pass record: each one marks the PR ready and hands it to the
	// human, so a PR reworked after review counts again.
	ReviewRounds float64 `json:"review_rounds"`
}

// bead gathers one bead's records, in ledger order.
type bead struct {
	id   string
	recs []ledger.Record
}

// Build rolls recs and the usage lines up into a Report.
func Build(recs []ledger.Record, uses []Use, o Options) Report {
	beads := group(recs)
	r := Report{Runs: len(recs), Beads: len(beads)}
	r.Quota = quota(beads)
	r.Cycle = cycle(beads)
	r.Parks = parks(recs, beads)
	r.Retries = retries(recs, beads)
	r.Rejects = rejects(recs)
	r.Memory = memory(recs, uses, o)
	r.Comparison = compare(beads, r.Memory.Runs, o.MinSample)
	return r
}

func group(recs []ledger.Record) []bead {
	var beads []bead
	at := map[string]int{}
	for _, rec := range recs {
		i, ok := at[rec.Bead]
		if !ok {
			i = len(beads)
			at[rec.Bead] = i
			beads = append(beads, bead{id: rec.Bead})
		}
		beads[i].recs = append(beads[i].recs, rec)
	}
	return beads
}

// final is the bead's last merged or rejected review record, or nil.
func (b bead) final() *ledger.Record {
	for i := len(b.recs) - 1; i >= 0; i-- {
		if o := stage.EventKind(b.recs[i].Outcome); o == stage.Merged || o == stage.Rejected {
			return &b.recs[i]
		}
	}
	return nil
}

func (b bead) count(outcome stage.EventKind) int {
	n := 0
	for _, rec := range b.recs {
		if stage.EventKind(rec.Outcome) == outcome {
			n++
		}
	}
	return n
}

func (b bead) parked() bool { return slices.ContainsFunc(b.recs, parks1) }

func quota(beads []bead) Quota {
	q := Quota{Merged: []MergedPR{}}
	for _, b := range beads {
		f := b.final()
		if f == nil || stage.EventKind(f.Outcome) != stage.Merged {
			continue
		}
		m := MergedPR{PR: f.PR, Bead: b.id}
		if m.PR == 0 {
			m.PR = ledger.LastPR(b.recs, b.id)
		}
		for _, rec := range b.recs {
			five, ok1 := delta(rec.Before.FiveHour, rec.After.FiveHour)
			seven, ok2 := delta(rec.Before.SevenDay, rec.After.SevenDay)
			if !ok1 || !ok2 {
				m.Unknown++
				continue
			}
			m.FiveHour += five
			m.SevenDay += seven
		}
		q.Merged = append(q.Merged, m)
		q.MeanFiveHour += m.FiveHour
		q.MeanSevenDay += m.SevenDay
	}
	if n := float64(len(q.Merged)); n > 0 {
		q.MeanFiveHour /= n
		q.MeanSevenDay /= n
	}
	return q
}

// delta is the usage one run added to a window: never negative, since a
// window that reset mid-run reads lower afterwards.
func delta(before, after *float64) (float64, bool) {
	if before == nil || after == nil {
		return 0, false
	}
	return max(*after-*before, 0), true
}

func cycle(beads []bead) Cycle {
	c := Cycle{Beads: []BeadCycle{}}
	var finished []int64
	for _, b := range beads {
		bc := BeadCycle{Bead: b.id, State: "open"}
		start := b.recs[0].Start
		end := b.recs[len(b.recs)-1].End
		if f := b.final(); f != nil {
			bc.State = f.Outcome
			end = f.End
		}
		bc.Seconds = int64(max(end.Sub(start), 0) / time.Second)
		if bc.State != "open" {
			finished = append(finished, bc.Seconds)
		}
		c.Beads = append(c.Beads, bc)
	}
	c.Finished = len(finished)
	if len(finished) > 0 {
		slices.Sort(finished)
		mid := len(finished) / 2
		c.MedianSeconds = finished[mid]
		if len(finished)%2 == 0 {
			c.MedianSeconds = (finished[mid-1] + finished[mid]) / 2
		}
	}
	return c
}

// parks1 reports whether rec's outcome parked its bead for a human: a vague
// spec or a session's question. A usage limit is not a park of the bead.
func parks1(rec ledger.Record) bool {
	o := stage.EventKind(rec.Outcome)
	return o == stage.NeedsHuman || o == stage.SpecVague
}

func parks(recs []ledger.Record, beads []bead) Parks {
	p := Parks{Beads: len(beads), Reasons: []Reason{}}
	for _, b := range beads {
		if b.parked() {
			p.Parked++
		}
	}
	if p.Beads > 0 {
		p.Rate = float64(p.Parked) / float64(p.Beads)
	}
	var reasons []string
	for _, rec := range recs {
		switch {
		case rec.Outcome == ledger.UsageLimit:
			p.UsageLimits++
		case parks1(rec):
			reasons = append(reasons, rec.Reason)
		}
	}
	p.Reasons = tally(reasons)
	return p
}

func retries(recs []ledger.Record, beads []bead) Retries {
	r := Retries{Beads: []BeadRetries{}, Distribution: []Bucket{}}
	byFails := map[int]int{}
	for _, b := range beads {
		br := BeadRetries{Bead: b.id}
		verified := false
		for _, rec := range b.recs {
			br.MaxRetries = max(br.MaxRetries, rec.Retries)
			verified = verified || rec.Stage == string(stage.Verify)
		}
		br.VerifyFails = b.count(stage.VerifyFail)
		if !verified && br.MaxRetries == 0 {
			continue
		}
		r.Beads = append(r.Beads, br)
		r.VerifyFails += br.VerifyFails
		byFails[br.VerifyFails]++
	}
	for _, n := range slices.Sorted(maps.Keys(byFails)) {
		r.Distribution = append(r.Distribution, Bucket{VerifyFails: n, Beads: byFails[n]})
	}
	for _, rec := range recs {
		if rec.TimedOut {
			r.TimedOut++
		}
	}
	return r
}

func rejects(recs []ledger.Record) Rejects {
	var reasons []string
	for _, rec := range recs {
		if stage.EventKind(rec.Outcome) == stage.Rejected {
			reasons = append(reasons, rec.Reason)
		}
	}
	return Rejects{Count: len(reasons), Reasons: tally(reasons)}
}

// tally groups reasons by their first line, most frequent first, then by
// text.
func tally(reasons []string) []Reason {
	counts := map[string]int{}
	for _, s := range reasons {
		counts[short(s)]++
	}
	out := []Reason{}
	for s, n := range counts {
		out = append(out, Reason{Reason: s, Count: n})
	}
	slices.SortFunc(out, func(a, b Reason) int {
		return cmp.Or(cmp.Compare(b.Count, a.Count), cmp.Compare(a.Reason, b.Reason))
	})
	return out
}

func short(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	s = strings.TrimSpace(s)
	if len(s) > maxReason {
		s = strings.ToValidUTF8(s[:maxReason], "") + "…"
	}
	if s == "" {
		return "(no reason)"
	}
	return s
}

// memory joins each LLM stage run to its usage lines. A run takes the lines
// whose agent_session is its ledger session when any line carries that id;
// otherwise the lines on its branch timed within its start and end that no
// run has claimed by session. A line counts for one run at most.
func memory(recs []ledger.Record, uses []Use, o Options) Memory {
	var calls []Use
	for _, u := range uses {
		if u.Context() {
			calls = append(calls, u)
		}
	}
	var runs []ledger.Record
	for _, rec := range recs {
		if rec.Session != "" && (o.LLMStages == nil || slices.Contains(o.LLMStages, stage.Stage(rec.Stage))) {
			runs = append(runs, rec)
		}
	}

	owner := make([]int, len(calls)) // index into runs + 1; 0 = unclaimed
	join := make([]string, len(runs))
	sessions := map[string]int{}
	for i, rec := range runs {
		if _, dup := sessions[rec.Session]; !dup {
			sessions[rec.Session] = i
		}
	}
	for j, u := range calls {
		if i, ok := sessions[u.AgentSession]; ok && u.AgentSession != "" {
			owner[j] = i + 1
			join[i] = "session"
		}
	}
	if o.Branch != nil {
		for i, rec := range runs {
			if join[i] != "" {
				continue
			}
			branch := o.Branch(rec.Bead, stage.Stage(rec.Stage))
			for j, u := range calls {
				if owner[j] == 0 && u.Branch == branch && !u.Time.Before(rec.Start) && !u.Time.After(rec.End) {
					owner[j] = i + 1
					join[i] = "branch"
				}
			}
		}
	}

	m := Memory{Runs: []Run{}}
	t := &m.Totals
	for i, rec := range runs {
		run := Run{Bead: rec.Bead, Stage: rec.Stage, Session: rec.Session, Join: cmp.Or(join[i], "none")}
		commit, known := o.Commits[rec.Session]
		before := 0
		for j, u := range calls {
			if owner[j] != i+1 {
				continue
			}
			run.ContextCalls++
			if commit.None || u.Time.Before(commit.At) {
				before++
			}
			if u.Items == 0 && !u.Spec {
				run.EmptyBundles++
			}
			if u.Component == "" {
				run.Unresolved++
			}
		}
		if known {
			run.BeforeCommit = &before
			t.BeforeCommit += before
		} else {
			t.CommitUnknown++
		}
		m.Runs = append(m.Runs, run)
		t.Runs++
		t.ContextCalls += run.ContextCalls
		t.EmptyBundles += run.EmptyBundles
		t.Unresolved += run.Unresolved
		if run.ContextCalls > 0 {
			t.RunsWithContext++
		}
		switch run.Join {
		case "session":
			t.BySession++
		case "branch":
			t.ByBranch++
		}
	}
	for _, own := range owner {
		if own == 0 {
			t.Unattributed++
		}
	}
	return m
}

func compare(beads []bead, runs []Run, minSample int) Comparison {
	if minSample <= 0 {
		minSample = DefaultMinSample
	}
	c := Comparison{MinSample: minSample}
	loaded := map[string]bool{}
	for _, run := range runs {
		loaded[run.Bead] = loaded[run.Bead] || run.ContextCalls > 0
	}
	var in, out []bead
	for _, b := range beads {
		l, ok := loaded[b.id]
		switch {
		case !ok:
		case l:
			in = append(in, b)
		default:
			out = append(out, b)
		}
	}
	c.Loaded.N, c.None.N = len(in), len(out)
	c.Comparable = len(in) >= minSample && len(out) >= minSample
	if c.Comparable {
		c.Loaded.Outcomes = outcomes(in)
		c.None.Outcomes = outcomes(out)
	}
	return c
}

func outcomes(beads []bead) *Outcomes {
	o := &Outcomes{}
	parked := 0
	for _, b := range beads {
		o.VerifyRetries += float64(b.count(stage.VerifyFail))
		o.ReviewRounds += float64(b.count(stage.VerifyPass))
		if b.parked() {
			parked++
		}
		if f := b.final(); f != nil {
			if stage.EventKind(f.Outcome) == stage.Merged {
				o.Merged++
			} else {
				o.Rejected++
			}
		}
	}
	n := float64(len(beads))
	o.VerifyRetries /= n
	o.ReviewRounds /= n
	o.ParkRate = float64(parked) / n
	return o
}
