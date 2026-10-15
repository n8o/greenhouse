package report

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"
)

// TooFew is the line Text prints in place of a comparison whose groups are
// below the minimum sample.
const TooFew = "too few runs to compare (need %d per group)"

// Text writes r as plain-text tables in the style of `greenhouse status`.
func Text(w io.Writer, r Report) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	p := func(format string, args ...any) { fmt.Fprintf(tw, format, args...) }

	p("ledger: %d runs over %d beads\n", r.Runs, r.Beads)

	p("\nquota per merged PR: %d merged\n", len(r.Quota.Merged))
	if len(r.Quota.Merged) > 0 {
		p("PR\tBEAD\t5H PTS\t7D PTS\tUNKNOWN RUNS\n")
		for _, m := range r.Quota.Merged {
			p("#%d\t%s\t%.1f\t%.1f\t%d\n", m.PR, m.Bead, m.FiveHour, m.SevenDay, m.Unknown)
		}
		p("mean\t\t%.1f\t%.1f\n", r.Quota.MeanFiveHour, r.Quota.MeanSevenDay)
	}

	p("\ncycle time: %d beads, %d finished, median %s\n", len(r.Cycle.Beads), r.Cycle.Finished, dur(r.Cycle.MedianSeconds))
	if len(r.Cycle.Beads) > 0 {
		p("BEAD\tSTATE\tCYCLE\n")
		for _, c := range r.Cycle.Beads {
			p("%s\t%s\t%s\n", c.Bead, c.State, dur(c.Seconds))
		}
	}

	p("\nparks: %d of %d beads (%s) · usage-limit stops: %d (not parks)\n",
		r.Parks.Parked, r.Parks.Beads, pct(r.Parks.Rate), r.Parks.UsageLimits)
	reasons(p, r.Parks.Reasons)

	p("\nretries: %d verify failures · %d timed-out runs\n", r.Retries.VerifyFails, r.Retries.TimedOut)
	if len(r.Retries.Beads) > 0 {
		p("BEAD\tMAX RETRIES\tVERIFY FAILS\n")
		for _, b := range r.Retries.Beads {
			p("%s\t%d\t%d\n", b.Bead, b.MaxRetries, b.VerifyFails)
		}
		var dist []string
		for _, b := range r.Retries.Distribution {
			dist = append(dist, fmt.Sprintf("%d fails: %d", b.VerifyFails, b.Beads))
		}
		p("beads by verify fails: %s\n", strings.Join(dist, " · "))
	}

	p("\nrejects: %d\n", r.Rejects.Count)
	reasons(p, r.Rejects.Reasons)

	t := r.Memory.Totals
	p("\nscoped memory: %d of %d LLM runs loaded context · %d context calls (%d unattributed)\n",
		t.RunsWithContext, t.Runs, t.ContextCalls, t.Unattributed)
	p("before first commit: %d (%d runs unknown) · empty bundles: %d · unresolved paths: %d\n",
		t.BeforeCommit, t.CommitUnknown, t.EmptyBundles, t.Unresolved)
	if len(r.Memory.Runs) > 0 {
		p("BEAD\tSTAGE\tSESSION\tJOIN\tCALLS\tBEFORE COMMIT\tEMPTY\tUNRESOLVED\n")
		for _, run := range r.Memory.Runs {
			before := "?"
			if run.BeforeCommit != nil {
				before = fmt.Sprint(*run.BeforeCommit)
			}
			p("%s\t%s\t%s\t%s\t%d\t%s\t%d\t%d\n", run.Bead, run.Stage, run.Session, run.Join,
				run.ContextCalls, before, run.EmptyBundles, run.Unresolved)
		}
	}

	c := r.Comparison
	p("\nloaded context vs none (by bead): loaded n=%d · none n=%d\n", c.Loaded.N, c.None.N)
	if !c.Comparable {
		p(TooFew+"\n", c.MinSample)
	} else {
		p("GROUP\tN\tVERIFY RETRIES\tPARK RATE\tMERGED\tREJECTED\tREVIEW ROUNDS\n")
		for _, g := range []struct {
			name string
			g    Group
		}{{"loaded", c.Loaded}, {"none", c.None}} {
			o := g.g.Outcomes
			p("%s\t%d\t%.2f\t%s\t%d\t%d\t%.2f\n", g.name, g.g.N, o.VerifyRetries, pct(o.ParkRate), o.Merged, o.Rejected, o.ReviewRounds)
		}
	}
	return tw.Flush()
}

func reasons(p func(string, ...any), rs []Reason) {
	if len(rs) == 0 {
		return
	}
	p("COUNT\tREASON\n")
	for _, r := range rs {
		p("%d\t%s\n", r.Count, r.Reason)
	}
}

func dur(seconds int64) string { return (time.Duration(seconds) * time.Second).String() }

func pct(rate float64) string { return fmt.Sprintf("%.0f%%", rate*100) }
