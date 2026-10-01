package gate

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// FallbackPark is how long a recognised usage-limit stop parks launches when
// its reset time cannot be read: one 5-hour window.
const FallbackPark = 5 * time.Hour

// The usage-limit stops Claude Code reports, recorded in
// testdata/usage-limit-messages.txt. The matcher is strict: a line of the
// session's output must be one of these shapes in full, so a message that
// merely quotes or discusses a limit does not park the factory.
var (
	// Current Claude Code, per its error reference:
	//   You've hit your session limit · resets 3:45pm
	//   You've hit your weekly limit · resets Mon 12:00am
	hitLimit = regexp.MustCompile(`^You(?:'|’)ve hit your (session|weekly|Opus|Sonnet) limit [·∙•] resets (.+)$`)
	// Earlier Claude Code:
	//   Claude usage limit reached. Your limit will reset at 3pm (America/New_York).
	limitReached = regexp.MustCompile(`^Claude usage limit reached\. Your limit will reset at (.+?)\.?$`)
	// Earliest Claude Code, as written to the transcript:
	//   Claude AI usage limit reached|1759262400
	limitEpoch = regexp.MustCompile(`^Claude AI usage limit reached\|(\d{9,11})$`)

	// resetAt is the reset in the first two shapes: an optional weekday, a
	// 12-hour clock time and an optional IANA zone.
	resetAt = regexp.MustCompile(`^(?:(Mon|Tue|Wed|Thu|Fri|Sat|Sun) )?(\d{1,2})(?::(\d{2}))? ?(am|pm)(?: \(([A-Za-z]+(?:/[A-Za-z0-9_+-]+)+|UTC)\))?$`)
)

// ParseUsageLimit looks for a usage-limit stop in a session's output and
// returns the Park it implies. local is the zone a reset time without one
// is read in: the host's, where Claude Code printed it. A recognised stop
// whose reset cannot be read parks for FallbackPark rather than not at all.
func ParseUsageLimit(output string, now time.Time, local *time.Location) (Park, bool) {
	for line := range strings.Lines(output) {
		line = strings.TrimSpace(line)
		if m := limitEpoch.FindStringSubmatch(line); m != nil {
			secs, _ := strconv.ParseInt(m[1], 10, 64)
			return Park{Until: time.Unix(secs, 0), Reason: line}, true
		}
		var when string
		if m := hitLimit.FindStringSubmatch(line); m != nil {
			when = m[2]
		} else if m := limitReached.FindStringSubmatch(line); m != nil {
			when = m[1]
		} else {
			continue
		}
		until, err := nextReset(when, now, local)
		if err != nil {
			return Park{Until: now.Add(FallbackPark), Reason: fmt.Sprintf("%s (%v; parked %s)", line, err, FallbackPark)}, true
		}
		return Park{Until: until, Reason: line}, true
	}
	return Park{}, false
}

var weekdays = map[string]time.Weekday{
	"Sun": time.Sunday, "Mon": time.Monday, "Tue": time.Tuesday, "Wed": time.Wednesday,
	"Thu": time.Thursday, "Fri": time.Friday, "Sat": time.Saturday,
}

// nextReset returns the first time after now that matches when ("3pm",
// "3:45pm", "Mon 12:00am", "3pm (Europe/Paris)").
func nextReset(when string, now time.Time, local *time.Location) (time.Time, error) {
	m := resetAt.FindStringSubmatch(when)
	if m == nil {
		return time.Time{}, fmt.Errorf("unrecognised reset time %q", when)
	}
	hour, _ := strconv.Atoi(m[2])
	minute := 0
	if m[3] != "" {
		minute, _ = strconv.Atoi(m[3])
	}
	if hour < 1 || hour > 12 || minute > 59 {
		return time.Time{}, fmt.Errorf("invalid reset time %q", when)
	}
	hour %= 12
	if m[4] == "pm" {
		hour += 12
	}
	loc := local
	if m[5] != "" {
		l, err := time.LoadLocation(m[5])
		if err != nil {
			return time.Time{}, fmt.Errorf("reset time zone %q: %w", m[5], err)
		}
		loc = l
	}
	n := now.In(loc)
	for day := 0; day <= 7; day++ {
		t := time.Date(n.Year(), n.Month(), n.Day()+day, hour, minute, 0, 0, loc)
		if !t.After(now) {
			continue
		}
		if m[1] != "" && t.Weekday() != weekdays[m[1]] {
			continue
		}
		return t, nil
	}
	return time.Time{}, fmt.Errorf("no reset time matches %q", when) // unreachable
}
