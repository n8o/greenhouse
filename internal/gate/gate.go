// Package gate decides whether the scheduler may launch another stage
// session (DESIGN.md "Gates", ADR-0002, ADR-0004). The decisions are pure
// functions of explicit inputs (a usage reading, counts, the time), so they
// are table-tested; probe.go holds the thin adapters that gather the inputs.
package gate

import (
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/n8o/greenhouse/internal/config"
	"github.com/n8o/greenhouse/internal/stage"
)

// Gate names, as they appear in a Decision.
const (
	QuotaGate   = "quota"
	WIPGate     = "wip"
	BacklogGate = "backlog"
)

// MaxReadingAge is how old a usage reading may be before the quota gate
// stops trusting it and falls back to reactive mode. agent-deck flags its
// own cached reading stale somewhere between 10 and 15 minutes; this is the
// outer bound, in case its flag is ever missing.
const MaxReadingAge = 15 * time.Minute

// Decision is one gate's verdict.
type Decision struct {
	Gate   string
	Allow  bool
	Reason string // human-readable, for status, doctor and the ledger
	// Until is when a denial is expected to lift (a window reset or a
	// park), or zero if unknown.
	Until time.Time
	// Reactive is set when the quota gate allowed without a fresh reading:
	// only a usage-limit stop reported by a session will hold launches.
	Reactive bool
}

func (d Decision) String() string {
	verdict := "allow"
	if !d.Allow {
		verdict = "deny"
	}
	return fmt.Sprintf("%s: %s: %s", d.Gate, verdict, d.Reason)
}

// Window is one Claude usage window from agent-deck's cache.
type Window struct {
	UsedPercentage float64   // 0..100
	ResetsAt       time.Time // zero if unknown
}

// used is the window's usage at now. Claude Code drops a window from its
// statusLine payload once the window resets, so a cached window whose reset
// has passed is empty rather than full.
func (w *Window) used(now time.Time) float64 {
	if w == nil || (!w.ResetsAt.IsZero() && !now.Before(w.ResetsAt)) {
		return 0
	}
	return w.UsedPercentage
}

// Reading is Claude's entry in `agent-deck usage --json`. A nil *Reading
// means agent-deck has none: the statusLine is not wired to
// `agent-deck usage ingest claude`, or it has not run yet.
type Reading struct {
	FiveHour  *Window // nil if the payload carried no 5-hour window
	SevenDay  *Window // nil if the payload carried no 7-day window
	UpdatedAt time.Time
	Stale     bool // agent-deck's own staleness flag
}

// freshness returns "" if r can gate launches at now, or why it cannot.
func (r *Reading) freshness(now time.Time) string {
	switch {
	case r == nil:
		return "no usage reading (is the statusLine wired to `agent-deck usage ingest claude`? see `greenhouse doctor`)"
	case r.FiveHour == nil && r.SevenDay == nil:
		return "usage reading has no windows"
	case r.UpdatedAt.IsZero():
		return "usage reading has no timestamp"
	case r.Stale || now.Sub(r.UpdatedAt) > MaxReadingAge:
		return fmt.Sprintf("usage reading is stale (updated %s ago)", now.Sub(r.UpdatedAt).Round(time.Second))
	}
	return ""
}

// Park holds launches until a usage-limit stop resets. It comes from
// ParseUsageLimit on a session's output; the zero Park holds nothing.
type Park struct {
	Until  time.Time
	Reason string
}

// Active reports whether p still holds launches at now.
func (p Park) Active(now time.Time) bool { return now.Before(p.Until) }

// Quota is the ADR-0002 gate. An active park denies whatever the reading
// says, because a session that hit the limit is the most direct evidence
// there is. Otherwise a fresh reading denies at or above either threshold.
// Without a fresh reading the gate allows in reactive mode, and the next
// usage-limit stop parks the scheduler.
func Quota(r *Reading, park Park, q config.Quota, now time.Time) Decision {
	d := Decision{Gate: QuotaGate}
	if park.Active(now) {
		d.Reason = fmt.Sprintf("parked until %s: %s", park.Until.Format(time.RFC3339), park.Reason)
		d.Until = park.Until
		return d
	}
	if why := r.freshness(now); why != "" {
		d.Allow, d.Reactive = true, true
		d.Reason = "reactive mode: " + why + "; a usage-limit stop will park launches until its reset"
		return d
	}
	five, week := r.FiveHour.used(now), r.SevenDay.used(now)
	switch {
	case five/100 >= q.FiveHourMax:
		d.Reason = fmt.Sprintf("5-hour usage %s is at or above five_hour_max %s", pct(five), pct(q.FiveHourMax*100))
		d.Until = r.FiveHour.ResetsAt
	case week/100 >= q.WeeklyShare:
		d.Reason = fmt.Sprintf("7-day usage %s is at or above weekly_share %s", pct(week), pct(q.WeeklyShare*100))
		d.Until = r.SevenDay.ResetsAt
	default:
		d.Allow = true
		d.Reason = fmt.Sprintf("5-hour %s < %s, 7-day %s < %s",
			pct(five), pct(q.FiveHourMax*100), pct(week), pct(q.WeeklyShare*100))
	}
	return d
}

// pct formats a percentage to at most two decimals, so 79.9 never reads as
// 80.
func pct(v float64) string {
	return strconv.FormatFloat(math.Round(v*100)/100, 'f', -1, 64) + "%"
}

// WIP denies a new implement launch while running implement sessions are at
// or above max. A count that could not be taken denies: the gate fails
// closed rather than guess.
func WIP(running int, err error, max int) Decision {
	return capGate(WIPGate, "running implement sessions", running, err, max, "wip.implement")
}

// Backlog is the review-backlog gate (ADR-0004): it denies while open
// greenhouse PRs are at or above max. Like WIP it fails closed.
func Backlog(open int, err error, max int) Decision {
	return capGate(BacklogGate, "open greenhouse PRs", open, err, max, "wip.open_prs")
}

func capGate(gate, what string, n int, err error, max int, key string) Decision {
	d := Decision{Gate: gate}
	switch {
	case err != nil:
		d.Reason = fmt.Sprintf("cannot count %s: %v", what, err)
	case n >= max:
		d.Reason = fmt.Sprintf("%d %s is at or above %s %d", n, what, key, max)
	default:
		d.Allow = true
		d.Reason = fmt.Sprintf("%d %s < %s %d", n, what, key, max)
	}
	return d
}

// Inputs is everything the gates read on one tick.
type Inputs struct {
	Now     time.Time
	Quota   config.Quota
	WIP     config.WIP
	Reading *Reading
	// ReadingErr is a failed read of the usage cache. It is treated like a
	// missing reading, so the quota gate goes reactive.
	ReadingErr   error
	Park         Park
	Implement    int // running implement sessions
	ImplementErr error
	OpenPRs      int // open greenhouse PRs
	OpenPRsErr   error
}

// Evaluate runs the gates that apply to launching a next-stage session, in a
// fixed order. Quota gates every stage session; wip and backlog gate only
// implement, the stage that opens PRs (ADR-0004).
func Evaluate(in Inputs, next stage.Stage) []Decision {
	reading := in.Reading
	if in.ReadingErr != nil {
		reading = nil
	}
	q := Quota(reading, in.Park, in.Quota, in.Now)
	if in.ReadingErr != nil && q.Reactive {
		q.Reason = fmt.Sprintf("reactive mode: cannot read usage: %v; a usage-limit stop will park launches until its reset", in.ReadingErr)
	}
	if next != stage.Implement {
		return []Decision{q}
	}
	return []Decision{
		q,
		WIP(in.Implement, in.ImplementErr, in.WIP.Implement),
		Backlog(in.OpenPRs, in.OpenPRsErr, in.WIP.OpenPRs),
	}
}

// Allowed reports whether every decision allows; if not, it also returns the
// first denial.
func Allowed(ds []Decision) (bool, Decision) {
	for _, d := range ds {
		if !d.Allow {
			return false, d
		}
	}
	return true, Decision{}
}
