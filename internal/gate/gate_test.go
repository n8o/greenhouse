package gate

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/n8o/greenhouse/internal/config"
	"github.com/n8o/greenhouse/internal/stage"
)

var (
	now     = time.Unix(1790800060, 0) // a minute after the testdata reading
	fiveRst = time.Unix(1790810000, 0)
	weekRst = time.Unix(1791300000, 0)
	caps    = config.Quota{FiveHourMax: 0.8, WeeklyShare: 0.5}
)

func reading(five, week float64) *Reading {
	return &Reading{
		FiveHour:  &Window{UsedPercentage: five, ResetsAt: fiveRst},
		SevenDay:  &Window{UsedPercentage: week, ResetsAt: weekRst},
		UpdatedAt: now.Add(-time.Minute),
	}
}

func TestQuota(t *testing.T) {
	stale := reading(10, 10)
	stale.Stale = true
	old := reading(10, 10)
	old.UpdatedAt = now.Add(-MaxReadingAge - time.Second)
	edge := reading(10, 10)
	edge.UpdatedAt = now.Add(-MaxReadingAge)
	undated := reading(10, 10)
	undated.UpdatedAt = time.Time{}
	noWindows := &Reading{UpdatedAt: now}
	onlyWeek := &Reading{SevenDay: &Window{UsedPercentage: 49.9, ResetsAt: weekRst}, UpdatedAt: now}
	fiveReset := reading(95, 10)
	fiveReset.FiveHour.ResetsAt = now // the window rolled over since the reading
	noResetTime := reading(95, 10)
	noResetTime.FiveHour.ResetsAt = time.Time{}
	park := Park{Until: now.Add(time.Hour), Reason: "You've hit your session limit · resets 9:00pm"}
	expired := Park{Until: now, Reason: "old"}

	tests := []struct {
		name     string
		r        *Reading
		park     Park
		allow    bool
		reactive bool
		until    time.Time
		reason   string
	}{
		{"below both", reading(42.5, 31), Park{}, true, false, time.Time{}, "5-hour 42.5% < 80%, 7-day 31% < 50%"},
		{"5h just below", reading(79.9, 0), Park{}, true, false, time.Time{}, "5-hour 79.9% < 80%"},
		{"5h exactly at max", reading(80, 0), Park{}, false, false, fiveRst, "5-hour usage 80% is at or above five_hour_max 80%"},
		{"5h above max", reading(99, 0), Park{}, false, false, fiveRst, "at or above five_hour_max"},
		{"7d just below", reading(0, 49.99), Park{}, true, false, time.Time{}, "7-day 49.99% < 50%"},
		{"7d exactly at share", reading(0, 50), Park{}, false, false, weekRst, "7-day usage 50% is at or above weekly_share 50%"},
		{"both over: 5h reported", reading(100, 100), Park{}, false, false, fiveRst, "five_hour_max"},
		{"5h window already reset", fiveReset, Park{}, true, false, time.Time{}, "5-hour 0% < 80%"},
		{"reset time unknown keeps usage", noResetTime, Park{}, false, false, time.Time{}, "five_hour_max"},
		{"missing 5h window counts as reset", onlyWeek, Park{}, true, false, time.Time{}, "5-hour 0% < 80%, 7-day 49.9% < 50%"},
		{"missing reading: reactive", nil, Park{}, true, true, time.Time{}, "reactive mode: no usage reading"},
		{"stale flag: reactive", stale, Park{}, true, true, time.Time{}, "reactive mode: usage reading is stale"},
		{"too old: reactive", old, Park{}, true, true, time.Time{}, "usage reading is stale (updated 15m1s ago)"},
		{"exactly max age: fresh", edge, Park{}, true, false, time.Time{}, "5-hour 10% < 80%"},
		{"no timestamp: reactive", undated, Park{}, true, true, time.Time{}, "no timestamp"},
		{"no windows: reactive", noWindows, Park{}, true, true, time.Time{}, "has no windows"},
		{"park beats missing reading", nil, park, false, false, park.Until, "parked until "},
		{"park beats fresh headroom", reading(1, 1), park, false, false, park.Until, "session limit"},
		{"park expires at its reset", nil, expired, true, true, time.Time{}, "reactive mode"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := Quota(tt.r, tt.park, caps, now)
			if d.Gate != QuotaGate || d.Allow != tt.allow || d.Reactive != tt.reactive || !d.Until.Equal(tt.until) {
				t.Errorf("got %+v, want allow=%v reactive=%v until=%v", d, tt.allow, tt.reactive, tt.until)
			}
			if !strings.Contains(d.Reason, tt.reason) {
				t.Errorf("reason %q, want it to contain %q", d.Reason, tt.reason)
			}
		})
	}
}

func TestCapGates(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name   string
		gate   func(int, error, int) Decision
		n      int
		err    error
		max    int
		allow  bool
		reason string
	}{
		{"wip none running", WIP, 0, nil, 1, true, "0 running implement sessions < wip.implement 1"},
		{"wip at cap", WIP, 1, nil, 1, false, "1 running implement sessions is at or above wip.implement 1"},
		{"wip over cap", WIP, 3, nil, 2, false, "at or above wip.implement 2"},
		{"wip one below cap", WIP, 1, nil, 2, true, "< wip.implement 2"},
		{"wip count failed", WIP, 0, boom, 1, false, "cannot count running implement sessions: boom"},
		{"backlog empty", Backlog, 0, nil, 3, true, "0 open greenhouse PRs < wip.open_prs 3"},
		{"backlog one below", Backlog, 2, nil, 3, true, "2 open greenhouse PRs < wip.open_prs 3"},
		{"backlog at cap", Backlog, 3, nil, 3, false, "3 open greenhouse PRs is at or above wip.open_prs 3"},
		{"backlog count failed", Backlog, 0, boom, 3, false, "cannot count open greenhouse PRs: boom"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := tt.gate(tt.n, tt.err, tt.max)
			if d.Allow != tt.allow || d.Reason != "" && !strings.Contains(d.Reason, tt.reason) {
				t.Errorf("got %+v, want allow=%v reason containing %q", d, tt.allow, tt.reason)
			}
		})
	}
}

func TestEvaluate(t *testing.T) {
	base := Inputs{Now: now, Quota: caps, WIP: config.WIP{Implement: 1, OpenPRs: 3}, Reading: reading(10, 10)}
	tests := []struct {
		name  string
		edit  func(*Inputs)
		next  stage.Stage
		gates []string
		deny  string // first denying gate, "" if all allow
	}{
		{"implement all clear", func(*Inputs) {}, stage.Implement, []string{"quota", "wip", "backlog"}, ""},
		{"implement at wip cap", func(in *Inputs) { in.Implement = 1 }, stage.Implement, []string{"quota", "wip", "backlog"}, "wip"},
		{"implement backlog full", func(in *Inputs) { in.OpenPRs = 3 }, stage.Implement, []string{"quota", "wip", "backlog"}, "backlog"},
		{"quota denies first", func(in *Inputs) { in.Reading = reading(90, 0); in.OpenPRs = 3 }, stage.Implement, []string{"quota", "wip", "backlog"}, "quota"},
		{"spec ignores wip and backlog", func(in *Inputs) { in.Implement, in.OpenPRs = 5, 5 }, stage.Spec, []string{"quota"}, ""},
		{"learn is quota gated", func(in *Inputs) { in.Reading = reading(0, 60) }, stage.Learn, []string{"quota"}, "quota"},
		{"usage read error goes reactive", func(in *Inputs) { in.ReadingErr = errors.New("no agent-deck") }, stage.Spec, []string{"quota"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := base
			tt.edit(&in)
			ds := Evaluate(in, tt.next)
			var gates []string
			for _, d := range ds {
				gates = append(gates, d.Gate)
			}
			if strings.Join(gates, ",") != strings.Join(tt.gates, ",") {
				t.Errorf("gates %v, want %v", gates, tt.gates)
			}
			ok, first := Allowed(ds)
			if ok != (tt.deny == "") || first.Gate != tt.deny {
				t.Errorf("Allowed = %v, first denial %q; want denial %q (%v)", ok, first.Gate, tt.deny, ds)
			}
		})
	}
}

func TestEvaluateReadErrorReason(t *testing.T) {
	ds := Evaluate(Inputs{Now: now, Quota: caps, Reading: reading(99, 99), ReadingErr: errors.New("exit 1")}, stage.Spec)
	if !ds[0].Allow || !ds[0].Reactive || !strings.Contains(ds[0].Reason, "cannot read usage: exit 1") {
		t.Errorf("got %+v, want a reactive allow naming the read error", ds[0])
	}
}

func TestDecisionString(t *testing.T) {
	d := WIP(1, nil, 1)
	if got, want := d.String(), "wip: deny: 1 running implement sessions is at or above wip.implement 1"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}
