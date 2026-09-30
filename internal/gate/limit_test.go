package gate

import (
	"os"
	"strings"
	"testing"
	"time"
)

// lines returns the non-comment, non-blank lines of a testdata file.
func lines(t *testing.T, name string) []string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for l := range strings.Lines(string(b)) {
		l = strings.TrimSpace(l)
		if l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out
}

func TestParseUsageLimitRecordedShapes(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	// Wed 2026-09-30 14:00 in New York.
	at := time.Date(2026, 9, 30, 14, 0, 0, 0, ny)
	want := []time.Time{
		time.Date(2026, 9, 30, 15, 45, 0, 0, ny),
		time.Date(2026, 10, 5, 0, 0, 0, 0, ny), // next Monday midnight
		time.Date(2026, 9, 30, 15, 45, 0, 0, ny),
		time.Date(2026, 9, 30, 15, 45, 0, 0, ny),
		time.Date(2026, 9, 30, 15, 0, 0, 0, ny),
		time.Unix(1790820000, 0),
	}
	msgs := lines(t, "usage-limit-messages.txt")
	if len(msgs) != len(want) {
		t.Fatalf("%d recorded messages, %d expectations", len(msgs), len(want))
	}
	for i, msg := range msgs {
		p, ok := ParseUsageLimit(msg, at, ny)
		if !ok {
			t.Errorf("%q: not recognised", msg)
			continue
		}
		if !p.Until.Equal(want[i]) || p.Reason != msg {
			t.Errorf("%q: got %v (%q), want %v", msg, p.Until, p.Reason, want[i])
		}
	}
}

func TestParseUsageLimitRejects(t *testing.T) {
	for _, msg := range lines(t, "not-usage-limit-messages.txt") {
		if p, ok := ParseUsageLimit(msg, now, time.UTC); ok {
			t.Errorf("%q: parked until %v, want no match", msg, p.Until)
		}
	}
}

func TestParseUsageLimit(t *testing.T) {
	paris, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		t.Fatal(err)
	}
	// Wed 2026-09-30 22:30 UTC.
	at := time.Date(2026, 9, 30, 22, 30, 0, 0, time.UTC)
	tests := []struct {
		name   string
		output string
		ok     bool
		until  time.Time
	}{
		{"later today", "You've hit your session limit · resets 11pm", true, time.Date(2026, 9, 30, 23, 0, 0, 0, time.UTC)},
		{"earlier time means tomorrow", "You've hit your session limit · resets 3:45pm", true, time.Date(2026, 10, 1, 15, 45, 0, 0, time.UTC)},
		{"exactly now means tomorrow", "You've hit your session limit · resets 10:30pm", true, time.Date(2026, 10, 1, 22, 30, 0, 0, time.UTC)},
		{"12am is midnight", "You've hit your session limit · resets 12am", true, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)},
		{"12pm is noon", "You've hit your session limit · resets 12pm", true, time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)},
		{"weekday today but past", "You've hit your weekly limit · resets Wed 9:00am", true, time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)},
		{"weekday today still ahead", "You've hit your weekly limit · resets Wed 11:00pm", true, time.Date(2026, 9, 30, 23, 0, 0, 0, time.UTC)},
		{"weekday tomorrow", "You've hit your weekly limit · resets Thu 1am", true, time.Date(2026, 10, 1, 1, 0, 0, 0, time.UTC)},
		{"zone given", "You've hit your session limit · resets 9am (Europe/Paris)", true, time.Date(2026, 10, 1, 9, 0, 0, 0, paris)},
		{"curly apostrophe", "You’ve hit your Opus limit · resets 11pm", true, time.Date(2026, 9, 30, 23, 0, 0, 0, time.UTC)},
		{"bullet variant", "You've hit your Sonnet limit ∙ resets 11pm", true, time.Date(2026, 9, 30, 23, 0, 0, 0, time.UTC)},
		{"among other lines", "Working on it.\n  You've hit your session limit · resets 11pm  \n", true, time.Date(2026, 9, 30, 23, 0, 0, 0, time.UTC)},
		{"legacy without zone or period", "Claude usage limit reached. Your limit will reset at 11pm", true, time.Date(2026, 9, 30, 23, 0, 0, 0, time.UTC)},
		{"epoch", "Claude AI usage limit reached|1790820000", true, time.Unix(1790820000, 0)},
		{"unreadable reset falls back", "You've hit your session limit · resets soon", true, at.Add(FallbackPark)},
		{"hour out of range falls back", "You've hit your session limit · resets 13pm", true, at.Add(FallbackPark)},
		{"unknown zone falls back", "You've hit your session limit · resets 9am (Nowhere/Land)", true, at.Add(FallbackPark)},

		{"quoted mid-line", "The session said \"You've hit your session limit · resets 11pm\" earlier.", false, time.Time{}},
		{"markdown quoted", "> You've hit your session limit · resets 11pm", false, time.Time{}},
		{"unknown limit kind", "You've hit your daily limit · resets 11pm", false, time.Time{}},
		{"lowercase variant", "you've hit your session limit · resets 11pm", false, time.Time{}},
		{"epoch too short", "Claude AI usage limit reached|123", false, time.Time{}},
		{"discussion", "If the usage limit is reached, greenhouse parks until reset.", false, time.Time{}},
		{"empty", "", false, time.Time{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, ok := ParseUsageLimit(tt.output, at, time.UTC)
			if ok != tt.ok || !p.Until.Equal(tt.until) {
				t.Errorf("got %v %v (%q), want %v %v", ok, p.Until, p.Reason, tt.ok, tt.until)
			}
			if ok && !p.Active(at) {
				t.Errorf("park until %v is not active at %v", p.Until, at)
			}
		})
	}
}
