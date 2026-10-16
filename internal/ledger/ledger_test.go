package ledger

import (
	"os"
	"testing"
	"time"
)

func TestAppendRecords(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	if recs, err := s.Records(); err != nil || recs != nil {
		t.Fatalf("empty ledger: %v %v", recs, err)
	}
	t0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	for _, r := range []Record{
		{Bead: "p-1", Stage: "spec", Session: "s1", Start: t0, End: t0, Outcome: "spec-done"},
		{Bead: "p-1", Stage: "implement", Session: "s2", Outcome: "implement-done", PR: 7},
		{Bead: "p-1", Stage: "verify", Outcome: "verify-fail", Reason: "log"},
		{Bead: "p-2", Stage: "verify", Outcome: "verify-fail"},
		{Bead: "p-1", Stage: "verify", Outcome: "verify-fail", Retries: 1},
	} {
		if err := s.Append(r); err != nil {
			t.Fatal(err)
		}
	}
	recs, err := s.Records()
	if err != nil || len(recs) != 5 {
		t.Fatalf("got %d records, %v", len(recs), err)
	}
	if !recs[0].Start.Equal(t0) || !Has(recs, "s2") || Has(recs, "s9") || Has(recs, "") {
		t.Errorf("round trip: %+v", recs[0])
	}
	if got := Retries(recs, "p-1"); got != 2 {
		t.Errorf("Retries = %d, want 2", got)
	}
	if got := LastPR(recs, "p-1"); got != 7 {
		t.Errorf("LastPR = %d", got)
	}
	if l := Last(recs, "p-2"); l == nil || l.Stage != "verify" {
		t.Errorf("Last = %+v", l)
	}
	// A new spec run starts a new attempt.
	recs = append(recs, Record{Bead: "p-1", Stage: "spec"})
	if got := Retries(recs, "p-1"); got != 0 {
		t.Errorf("Retries after spec = %d", got)
	}
}

func TestUnparkResetsRetries(t *testing.T) {
	recs := []Record{
		{Bead: "p-1", Stage: "implement", Outcome: "implement-done", PR: 7},
		{Bead: "p-1", Stage: "verify", Outcome: "verify-fail", PR: 7},
		{Bead: "p-1", Stage: "verify", Outcome: "verify-fail", PR: 7, Retries: 1},
	}
	if got := Retries(recs, "p-1"); got != 2 {
		t.Fatalf("Retries = %d, want 2", got)
	}
	recs = append(recs, Record{Bead: "p-1", Stage: ParkedStage, Outcome: Unparked, Resume: "verify"})
	if got := Retries(recs, "p-1"); got != 0 {
		t.Errorf("Retries after an unpark = %d, want 0", got)
	}
	if r := LastRun(recs, "p-1"); r == nil || r.Stage != "verify" || r.Retries != 1 {
		t.Errorf("LastRun skips the unpark: %+v", r)
	}
	if r := Last(recs, "p-1"); r == nil || r.IsRun() {
		t.Errorf("Last is the unpark: %+v", r)
	}
	if LastPR(recs, "p-1") != 7 || LastRun(recs, "p-2") != nil {
		t.Error("LastPR and LastRun")
	}
}

func TestTornLine(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	if err := s.Append(Record{Bead: "p-1", Stage: "spec"}); err != nil {
		t.Fatal(err)
	}
	f, _ := os.OpenFile(s.ledgerPath(), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"bead":"p-1","sta`)
	f.Close()
	if recs, err := s.Records(); err != nil || len(recs) != 1 {
		t.Errorf("torn last line: %d records, %v", len(recs), err)
	}
	// The next append starts a fresh line, so the torn one stays separate.
	if err := s.Append(Record{Bead: "p-1", Stage: "implement"}); err != nil {
		t.Fatal(err)
	}
	if recs, err := s.Records(); err != nil || len(recs) != 2 || recs[1].Stage != "implement" {
		t.Errorf("after torn line: %+v, %v", recs, err)
	}
}

func TestRunAndPark(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	if r, err := s.Run("p-1"); r != nil || err != nil {
		t.Fatalf("no run: %v %v", r, err)
	}
	if err := s.PutRun(Run{Bead: "p-1", Stage: "spec", Session: "s1"}); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.Run("p-1"); r == nil || r.Session != "s1" {
		t.Errorf("run: %+v", r)
	}
	if err := s.DeleteRun("p-1"); err != nil || s.DeleteRun("p-1") != nil {
		t.Errorf("delete: %v", err)
	}
	t0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s.PutPark(Park{Until: t0.Add(time.Hour), Reason: "a"})
	s.PutPark(Park{Until: t0, Reason: "earlier"})
	if p, _ := s.Park(); p.Reason != "a" {
		t.Errorf("a shorter park replaced a longer one: %+v", p)
	}
}
