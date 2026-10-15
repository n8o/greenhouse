package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/n8o/greenhouse/internal/ledger"
	"github.com/n8o/greenhouse/internal/stage"
)

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func at(min int) time.Time { return t0.Add(time.Duration(min) * time.Minute) }

func pt(v float64) *float64 { return &v }

func q(five, seven float64) ledger.Quota {
	return ledger.Quota{FiveHour: pt(five), SevenDay: pt(seven)}
}

func rec(bead, st string, start, end int, outcome string) ledger.Record {
	return ledger.Record{Bead: bead, Stage: st, Start: at(start), End: at(end), Outcome: outcome}
}

func branch(bead string, st stage.Stage) string { return fmt.Sprintf("feature/%s-%s", bead, st) }

func TestQuota(t *testing.T) {
	spec := rec("b1", "spec", 0, 10, "spec-done")
	spec.Before, spec.After = q(10, 20), q(14, 21)
	impl := rec("b1", "implement", 10, 30, "implement-done")
	impl.Before, impl.After = q(90, 22), q(5, 25) // 5h window reset mid-run: clamped to 0
	verify := rec("b1", "verify", 30, 31, "verify-pass")
	verify.Before = q(5, 25) // no after reading: unknown
	merged := rec("b1", "review", 31, 60, "merged")
	merged.PR = 7
	merged.Before, merged.After = ledger.Quota{FiveHour: pt(5)}, q(5, 25) // 7d before unknown
	open := rec("b2", "implement", 0, 5, "implement-done")
	open.Before, open.After = q(0, 0), q(50, 50)

	got := Build([]ledger.Record{spec, impl, verify, merged, open}, nil, Options{}).Quota
	want := Quota{
		Merged:       []MergedPR{{PR: 7, Bead: "b1", FiveHour: 4, SevenDay: 4, Unknown: 2}},
		MeanFiveHour: 4, MeanSevenDay: 4,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("quota = %+v, want %+v", got, want)
	}
}

func TestQuotaPRFromEarlierRecord(t *testing.T) {
	impl := rec("b1", "implement", 0, 10, "implement-done")
	impl.PR = 12
	got := Build([]ledger.Record{impl, rec("b1", "review", 10, 20, "merged")}, nil, Options{}).Quota.Merged
	if len(got) != 1 || got[0].PR != 12 || got[0].Unknown != 2 {
		t.Errorf("merged = %+v, want PR 12 with 2 unknown runs", got)
	}
}

func TestCycle(t *testing.T) {
	recs := []ledger.Record{
		rec("b1", "spec", 0, 10, "spec-done"),
		rec("b2", "spec", 5, 10, "spec-done"),
		rec("b1", "review", 10, 60, "merged"),
		rec("b3", "spec", 0, 30, "spec-done"),
		rec("b3", "review", 30, 120, "rejected"),
		rec("b3", "learn", 120, 130, "learn-done"),
		rec("b2", "implement", 10, 25, "implement-done"),
	}
	got := Build(recs, nil, Options{}).Cycle
	want := Cycle{
		Beads: []BeadCycle{
			{Bead: "b1", State: "merged", Seconds: 3600},
			{Bead: "b2", State: "open", Seconds: 1200},
			{Bead: "b3", State: "rejected", Seconds: 7200},
		},
		MedianSeconds: 5400, // of 3600 and 7200; open b2 is left out
		Finished:      2,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("cycle = %+v, want %+v", got, want)
	}
}

func TestParks(t *testing.T) {
	vague := rec("b1", "spec", 0, 1, "spec-vague")
	vague.Reason = "which API?\nmore detail"
	ask := rec("b2", "implement", 0, 1, "needs-human")
	ask.Reason = "which API?"
	ask2 := rec("b2", "implement", 2, 3, "needs-human")
	ask2.Reason = "  "
	recs := []ledger.Record{
		vague, ask, ask2,
		rec("b3", "implement", 0, 1, ledger.UsageLimit),
		rec("b3", "implement", 2, 3, ledger.UsageLimit),
		rec("b4", "spec", 0, 1, "spec-done"),
	}
	got := Build(recs, nil, Options{}).Parks
	want := Parks{Parked: 2, Beads: 4, Rate: 0.5, UsageLimits: 2, Reasons: []Reason{
		{Reason: "which API?", Count: 2},
		{Reason: "(no reason)", Count: 1},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parks = %+v, want %+v", got, want)
	}
}

func TestRetriesAndRejects(t *testing.T) {
	fail := func(bead string, retries int) ledger.Record {
		r := rec(bead, "verify", 0, 1, "verify-fail")
		r.Retries, r.Reason = retries, "go test failed"
		return r
	}
	impl := rec("b1", "implement", 1, 2, "implement-done")
	impl.Retries = 1
	impl.TimedOut = true
	rej := rec("b2", "review", 3, 4, "rejected")
	rej.Reason = "wrong approach"
	rej2 := rec("b3", "review", 3, 4, "rejected")
	rej2.Reason = strings.Repeat("x", 200)
	recs := []ledger.Record{
		fail("b1", 0), impl, fail("b1", 1),
		rec("b2", "verify", 0, 1, "verify-pass"), rej,
		rec("b3", "verify", 0, 1, "verify-pass"), rej2,
		rec("b4", "spec", 0, 1, "spec-done"), // never verified: not in retries
	}
	r := Build(recs, nil, Options{})
	wantRetries := Retries{
		Beads: []BeadRetries{
			{Bead: "b1", MaxRetries: 1, VerifyFails: 2},
			{Bead: "b2"},
			{Bead: "b3"},
		},
		VerifyFails:  2,
		Distribution: []Bucket{{VerifyFails: 0, Beads: 2}, {VerifyFails: 2, Beads: 1}},
		TimedOut:     1,
	}
	if !reflect.DeepEqual(r.Retries, wantRetries) {
		t.Errorf("retries = %+v, want %+v", r.Retries, wantRetries)
	}
	wantRejects := Rejects{Count: 2, Reasons: []Reason{
		{Reason: "wrong approach", Count: 1},
		{Reason: strings.Repeat("x", maxReason) + "…", Count: 1},
	}}
	if !reflect.DeepEqual(r.Rejects, wantRejects) {
		t.Errorf("rejects = %+v, want %+v", r.Rejects, wantRejects)
	}
}

func call(min int, src, branch, session string) Use {
	return Use{Time: at(min), Source: src, Branch: branch, AgentSession: session, Component: "cli", Items: 3}
}

func TestMemoryJoinBySession(t *testing.T) {
	impl := rec("b1", "implement", 0, 60, "implement-done")
	impl.Session = "s1"
	uses := []Use{
		call(5, "cli", "elsewhere", "s1"),          // session wins over branch and window
		call(500, "mcp", "", "s1"),                 // even outside the window
		call(6, "hook", "", "s1"),                  // an edit, not a context call
		call(7, "cli", "feature/b1-implement", ""), // the run joined by session, so branch lines are not its
	}
	uses[1].Items, uses[1].Component = 0, "" // empty bundle, unresolved path
	m := Build([]ledger.Record{impl}, uses, Options{Branch: branch,
		Commits: map[string]Commit{"s1": {At: at(10)}}}).Memory
	one := 1
	wantRuns := []Run{{Bead: "b1", Stage: "implement", Session: "s1", Join: "session",
		ContextCalls: 2, BeforeCommit: &one, EmptyBundles: 1, Unresolved: 1}}
	if !reflect.DeepEqual(m.Runs, wantRuns) {
		t.Errorf("runs = %s, want %s", js(m.Runs), js(wantRuns))
	}
	wantTotals := MemoryTotal{Runs: 1, RunsWithContext: 1, ContextCalls: 2, BeforeCommit: 1,
		EmptyBundles: 1, Unresolved: 1, BySession: 1, Unattributed: 1}
	if m.Totals != wantTotals {
		t.Errorf("totals = %+v, want %+v", m.Totals, wantTotals)
	}
}

func TestMemoryJoinByBranch(t *testing.T) {
	impl := rec("b1", "implement", 0, 60, "implement-done")
	impl.Session = "s1"
	uses := []Use{
		call(0, "cli", "feature/b1-implement", ""),   // at Start: in
		call(60, "cli", "feature/b1-implement", "x"), // at End: in; foreign agent_session is no session match
		call(61, "cli", "feature/b1-implement", ""),  // after End: out
		call(30, "cli", "feature/b1-spec", ""),       // another branch: out
	}
	m := Build([]ledger.Record{impl}, uses, Options{Branch: branch,
		Commits: map[string]Commit{"s1": {None: true}}}).Memory
	two := 2
	want := []Run{{Bead: "b1", Stage: "implement", Session: "s1", Join: "branch", ContextCalls: 2, BeforeCommit: &two}}
	if !reflect.DeepEqual(m.Runs, want) {
		t.Errorf("runs = %s, want %s", js(m.Runs), js(want))
	}
	if m.Totals.Unattributed != 2 || m.Totals.ByBranch != 1 {
		t.Errorf("totals = %+v, want 2 unattributed, 1 by branch", m.Totals)
	}
}

func TestMemoryMixedJoinsAndFilters(t *testing.T) {
	spec := rec("b1", "spec", 0, 10, "spec-done")
	spec.Session = "s1"
	impl := rec("b1", "implement", 10, 60, "implement-done")
	impl.Session = "s2"
	verify := rec("b1", "verify", 60, 61, "verify-pass") // no session: not an LLM run
	review := rec("b1", "review", 61, 70, "merged")
	review.Session = "odd" // not an LLM stage
	other := rec("b2", "implement", 0, 60, "implement-done")
	other.Session = "s3" // no lines at all
	uses := []Use{
		call(5, "cli", "feature/b1-spec", "s1"),
		call(20, "mcp", "feature/b1-implement", ""),
		call(65, "cli", "feature/b1-review", ""),
	}
	m := Build([]ledger.Record{spec, impl, verify, review, other}, uses, Options{
		LLMStages: []stage.Stage{stage.Spec, stage.Implement, stage.Learn}, Branch: branch,
	}).Memory
	var got []string
	for _, r := range m.Runs {
		got = append(got, fmt.Sprintf("%s/%s:%s:%d", r.Bead, r.Stage, r.Join, r.ContextCalls))
		if r.BeforeCommit != nil {
			t.Errorf("%s: before_first_commit = %d, want unknown", r.Session, *r.BeforeCommit)
		}
	}
	want := []string{"b1/spec:session:1", "b1/implement:branch:1", "b2/implement:none:0"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("runs = %v, want %v", got, want)
	}
	wantTotals := MemoryTotal{Runs: 3, RunsWithContext: 2, ContextCalls: 2, CommitUnknown: 3,
		BySession: 1, ByBranch: 1, Unattributed: 1}
	if m.Totals != wantTotals {
		t.Errorf("totals = %+v, want %+v", m.Totals, wantTotals)
	}
}

func TestMemoryNoBranchFunc(t *testing.T) {
	impl := rec("b1", "implement", 0, 60, "implement-done")
	impl.Session = "s1"
	m := Build([]ledger.Record{impl}, []Use{call(5, "cli", "feature/b1-implement", "")}, Options{}).Memory
	if m.Runs[0].Join != "none" || m.Totals.Unattributed != 1 {
		t.Errorf("without Branch the line must stay unattributed: %+v", m)
	}
}

// beads builds n beads that loaded context (loaded) or not, each merged
// after one verify retry, the first of each group parked once.
func beads(n int, loaded bool, prefix string) ([]ledger.Record, []Use) {
	var recs []ledger.Record
	var uses []Use
	for i := range n {
		id := fmt.Sprintf("%s%d", prefix, i)
		impl := rec(id, "implement", 0, 10, "implement-done")
		impl.Session = id + "-s"
		recs = append(recs, impl)
		if loaded {
			uses = append(uses, call(1, "cli", "", impl.Session))
		}
		if i == 0 {
			recs = append(recs, rec(id, "implement", 10, 11, "needs-human"))
		}
		recs = append(recs,
			rec(id, "verify", 11, 12, "verify-fail"),
			rec(id, "verify", 12, 13, "verify-pass"),
			rec(id, "review", 13, 20, "merged"))
	}
	return recs, uses
}

func TestComparison(t *testing.T) {
	const min = 3
	for _, tt := range []struct {
		name       string
		loaded     int
		none       int
		comparable bool
	}{
		{"both below", min - 1, min - 1, false},
		{"loaded below", min - 1, min, false},
		{"none below", min, min - 1, false},
		{"both at min", min, min, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r1, u1 := beads(tt.loaded, true, "l")
			r2, u2 := beads(tt.none, false, "n")
			unrun := rec("x", "verify", 0, 1, "verify-pass") // no LLM run: in neither group
			c := Build(append(append(r1, r2...), unrun), append(u1, u2...), Options{MinSample: min}).Comparison
			if c.Loaded.N != tt.loaded || c.None.N != tt.none || c.Comparable != tt.comparable || c.MinSample != min {
				t.Fatalf("comparison = %s", js(c))
			}
			if !tt.comparable {
				if c.Loaded.Outcomes != nil || c.None.Outcomes != nil {
					t.Errorf("outcomes given below the minimum sample: %s", js(c))
				}
				return
			}
			want := &Outcomes{VerifyRetries: 1, ParkRate: 1.0 / min, Merged: min, ReviewRounds: 1}
			if !reflect.DeepEqual(c.Loaded.Outcomes, want) || !reflect.DeepEqual(c.None.Outcomes, want) {
				t.Errorf("outcomes = %s, want %s each", js(c), js(want))
			}
		})
	}
}

func TestComparisonDefaultMinSample(t *testing.T) {
	if c := Build(nil, nil, Options{}).Comparison; c.MinSample != DefaultMinSample || c.Comparable {
		t.Errorf("comparison = %+v", c)
	}
}

func js(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// fixture is a small ledger that touches every section.
func fixture() Report {
	spec := rec("gr-1", "spec", 0, 10, "spec-done")
	spec.Session = "s1"
	spec.Before, spec.After = q(10, 20), q(12, 21)
	impl := rec("gr-1", "implement", 10, 40, "implement-done")
	impl.Session, impl.PR = "s2", 4
	impl.Before, impl.After = q(12, 21), q(20, 23)
	recs := []ledger.Record{
		spec, impl,
		rec("gr-1", "verify", 40, 41, "verify-pass"),
		rec("gr-1", "review", 41, 100, "merged"),
		rec("gr-2", "spec", 0, 5, "spec-vague"),
	}
	recs[4].Reason = "too vague"
	recs[4].Session = "s3"
	uses := []Use{call(3, "cli", "", "s1"), call(15, "mcp", "feature/gr-1-implement", "")}
	return Build(recs, uses, Options{
		LLMStages: []stage.Stage{stage.Spec, stage.Implement, stage.Learn},
		Branch:    branch,
		Commits:   map[string]Commit{"s2": {At: at(20)}},
	})
}

func TestTextGolden(t *testing.T) {
	var out bytes.Buffer
	if err := Text(&out, fixture()); err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "report.golden")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(golden, out.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != string(want) {
		t.Errorf("text output differs from %s (UPDATE_GOLDEN=1 to refresh):\n%s", golden, out.String())
	}
}

func TestTextComparable(t *testing.T) {
	r1, u1 := beads(1, true, "l")
	r2, u2 := beads(1, false, "n")
	var out bytes.Buffer
	if err := Text(&out, Build(append(r1, r2...), append(u1, u2...), Options{MinSample: 1})); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"GROUP   N  VERIFY RETRIES  PARK RATE  MERGED  REJECTED  REVIEW ROUNDS",
		"loaded  1  1.00            100%       1       0         1.00",
		"none    1  1.00            100%       1       0         1.00",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "too few") {
		t.Errorf("comparable report says too few:\n%s", out.String())
	}
}

func TestJSONRoundTrip(t *testing.T) {
	for name, r := range map[string]Report{"empty": Build(nil, nil, Options{}), "fixture": fixture()} {
		t.Run(name, func(t *testing.T) {
			data, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(data, []byte("null")) {
				t.Errorf("JSON has a null: %s", data)
			}
			if bytes.Contains(data, []byte(`"outcomes"`)) {
				t.Errorf("JSON has outcomes below the minimum sample: %s", data)
			}
			var back Report
			if err := json.Unmarshal(data, &back); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(back, r) {
				t.Errorf("round trip changed the report:\n got %s\nwant %s", js(back), js(r))
			}
		})
	}
}

func TestJSONKeys(t *testing.T) {
	data, _ := json.Marshal(Build(nil, nil, Options{}))
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"runs", "beads", "quota", "cycle", "parks", "retries", "rejects", "memory", "comparison"} {
		if _, ok := top[k]; !ok {
			t.Errorf("JSON missing key %q: %s", k, data)
		}
	}
	if !strings.Contains(string(top["comparison"]), `"comparable":false`) {
		t.Errorf("comparison = %s", top["comparison"])
	}
}
