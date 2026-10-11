// Package ledger keeps the scheduler's local run state under .greenhouse/
// (gitignored, ADR-0005): the append-only ledger of finished stage runs, the
// open run of each bead, and the usage-limit park. Everything else the
// scheduler knows comes from bd, agent-deck and gh, so this directory plus
// those tools rebuild the whole run.
//
// The ledger is the scheduler's commit point. A stage run's line is appended
// once, when its outcome is known; the scheduler then carries out the
// transition the outcome implies, and every effect of that transition is
// idempotent, so a crash after the append redoes them rather than losing or
// doubling the run.
package ledger

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Dir is the run-state directory, relative to the repo.
const Dir = ".greenhouse"

// Outcomes that are not stage event kinds.
const (
	// UsageLimit marks a run stopped by a Claude usage limit. It carries no
	// event: the stage is launched again once the park lifts.
	UsageLimit = "usage-limit"
)

// Quota is a usage reading at one moment, in percent of each window; nil
// fields were unknown.
type Quota struct {
	FiveHour *float64 `json:"five_hour,omitempty"`
	SevenDay *float64 `json:"seven_day,omitempty"`
}

// Record is one finished stage run: one line of ledger.jsonl.
type Record struct {
	Bead    string    `json:"bead"`
	Stage   string    `json:"stage"`
	Session string    `json:"session,omitempty"` // agent-deck session id; "" for verify and review
	Start   time.Time `json:"start"`
	End     time.Time `json:"end"`
	Before  Quota     `json:"quota_before"`
	After   Quota     `json:"quota_after"`
	// Retries is how many verify failures had already sent the bead back to
	// implement when this run started.
	Retries int `json:"retries"`
	// Outcome is the stage event kind the run produced, or UsageLimit.
	Outcome  string `json:"outcome"`
	Reason   string `json:"reason,omitempty"`
	PR       int    `json:"pr,omitempty"`
	TimedOut bool   `json:"timed_out,omitempty"` // stopped at its wall-clock limit
}

// Run is a bead's open stage run, kept in runs/<bead>.json from just before
// its launch until its Record is appended. Session is "" until the launch
// returns a session id.
type Run struct {
	Bead    string    `json:"bead"`
	Stage   string    `json:"stage"`
	Session string    `json:"session,omitempty"`
	Start   time.Time `json:"start"`
	Before  Quota     `json:"quota_before"`
	Retries int       `json:"retries"`
}

// Park holds launches until a usage-limit stop resets.
type Park struct {
	Until  time.Time `json:"until"`
	Reason string    `json:"reason"`
}

// Store is the run-state directory of one repo.
type Store struct {
	Dir string // <repo>/.greenhouse
}

// Open returns the Store of repo.
func Open(repo string) *Store { return &Store{Dir: filepath.Join(repo, Dir)} }

func (s *Store) ledgerPath() string         { return filepath.Join(s.Dir, "ledger.jsonl") }
func (s *Store) runPath(bead string) string { return filepath.Join(s.Dir, "runs", bead+".json") }
func (s *Store) parkPath() string           { return filepath.Join(s.Dir, "park.json") }

// Append adds r to the ledger.
func (s *Store) Append(r Record) error {
	line, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(s.ledgerPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	// After a torn append the file ends mid-line; start a fresh line so the
	// torn one stays a single bad line instead of corrupting this one.
	if st, err := f.Stat(); err == nil && st.Size() > 0 {
		last := make([]byte, 1)
		if r, err := os.Open(s.ledgerPath()); err == nil {
			r.ReadAt(last, st.Size()-1)
			r.Close()
		}
		if last[0] != '\n' {
			line = append([]byte{'\n'}, line...)
		}
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Records returns every ledger record in order; a missing ledger is empty.
// A line that is not a whole record is skipped: it is an append torn by a
// crash, and that run was never committed, so the scheduler collects it again.
func (s *Store) Records() ([]Record, error) {
	data, err := os.ReadFile(s.ledgerPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var recs []Record
	for line := range bytes.SplitSeq(data, []byte("\n")) {
		var r Record
		if len(bytes.TrimSpace(line)) == 0 || json.Unmarshal(line, &r) != nil {
			continue
		}
		recs = append(recs, r)
	}
	return recs, nil
}

// Run returns bead's open run, or nil.
func (s *Store) Run(bead string) (*Run, error) {
	var r Run
	ok, err := readJSON(s.runPath(bead), &r)
	if !ok || err != nil {
		return nil, err
	}
	return &r, nil
}

// PutRun saves r as its bead's open run.
func (s *Store) PutRun(r Run) error { return writeJSON(s.runPath(r.Bead), r) }

// DeleteRun removes bead's open run; a missing one is not an error.
func (s *Store) DeleteRun(bead string) error {
	err := os.Remove(s.runPath(bead))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// Park returns the saved usage-limit park; the zero Park if there is none.
func (s *Store) Park() (Park, error) {
	var p Park
	_, err := readJSON(s.parkPath(), &p)
	return p, err
}

// PutPark saves p unless the saved park already lasts longer.
func (s *Store) PutPark(p Park) error {
	cur, err := s.Park()
	if err != nil {
		return err
	}
	if !p.Until.After(cur.Until) {
		return nil
	}
	return writeJSON(s.parkPath(), p)
}

func readJSON(path string, v any) (bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	return true, nil
}

// writeJSON replaces path atomically, so a crash leaves the old or the new
// content, never half of one.
func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Last returns bead's most recent record, or nil.
func Last(recs []Record, bead string) *Record {
	for i := len(recs) - 1; i >= 0; i-- {
		if recs[i].Bead == bead {
			return &recs[i]
		}
	}
	return nil
}

// Has reports whether a record for session exists.
func Has(recs []Record, session string) bool {
	if session == "" {
		return false
	}
	for _, r := range recs {
		if r.Session == session {
			return true
		}
	}
	return false
}

// Retries counts bead's failed verify runs since its last spec run: how many
// times verify has sent it back to implement in the current attempt.
func Retries(recs []Record, bead string) int {
	n := 0
	for _, r := range recs {
		switch {
		case r.Bead != bead:
		case r.Stage == "spec":
			n = 0
		case r.Stage == "verify" && r.Outcome == "verify-fail":
			n++
		}
	}
	return n
}

// LastPR returns the PR of bead's most recent record that names one.
func LastPR(recs []Record, bead string) int {
	for i := len(recs) - 1; i >= 0; i-- {
		if recs[i].Bead == bead && recs[i].PR > 0 {
			return recs[i].PR
		}
	}
	return 0
}
