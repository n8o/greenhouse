// Package plan reads a Beads plan. It is the only package that talks to bd;
// callers depend on Source so tests can swap in a Fake.
package plan

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"slices"
	"strconv"
	"strings"
)

// StagePrefix marks the label that records a bead's stage, e.g. "stage:spec".
const StagePrefix = "stage:"

// ApprovedLabel marks a bead the human released to the factory (ADR-0004).
const ApprovedLabel = "factory:approved"

// Bead is the part of a bd issue greenhouse uses.
type Bead struct {
	ID     string   `json:"id"`
	Title  string   `json:"title"`
	Status string   `json:"status"`
	Labels []string `json:"labels"`
	// Description holds the bead's "Done when".
	Description string `json:"description"`
	// Acceptance and Design are what the spec stage records on the bead.
	Acceptance   string       `json:"acceptance_criteria"`
	Design       string       `json:"design"`
	Dependencies []Dependency `json:"dependencies"`
}

// Dependency is one edge of `bd list --json`: the bead waits on DependsOn.
type Dependency struct {
	DependsOn string `json:"depends_on_id"`
	Type      string `json:"type"`
}

// Approved reports whether the bead carries ApprovedLabel.
func (b Bead) Approved() bool { return slices.Contains(b.Labels, ApprovedLabel) }

// ExportDir holds the exported plan, one JSONL file per plan (ADR-0005).
const ExportDir = ".beads/plans/"

// Landed is the set of beads whose close reached the default branch: the
// beads closed in the plan export (ExportDir) at origin/<default>. It, and
// not the local bd database, is what "done" means (ADR-0010). A stage
// session closes its own bead inside its PR (ADR-0005), and in a shared bd
// database that close shows up long before the PR merges, if it ever does;
// only the merge puts the close on the default branch.
type Landed map[string]bool

// Done reports whether bead id is finished: its close is on the default
// branch.
func (l Landed) Done(id string) bool { return l[id] }

// ParseLanded reads exported plan files (JSONL, one bead per line) and
// returns the beads they hold as closed. A line that is not a bead is an
// error: a plan the scheduler cannot read must not quietly read as "nothing
// is done".
func ParseLanded(files ...[]byte) (Landed, error) {
	l := Landed{}
	for _, f := range files {
		for line := range bytes.Lines(f) {
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			var b struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			}
			if err := json.Unmarshal(line, &b); err != nil || b.ID == "" {
				return nil, fmt.Errorf("unreadable line under %s: %.80q", ExportDir, line)
			}
			if b.Status == "closed" {
				l[b.ID] = true
			}
		}
	}
	return l, nil
}

// Ready reports whether b can start: it is open, and every bead it is blocked
// by is Done on the default branch. A blocker closed only in the local bd
// database is not done, so a step never starts on top of an unmerged PR.
func Ready(b Bead, landed Landed) bool {
	if b.Status != "open" {
		return false
	}
	for _, d := range Blockers(b) {
		if !landed.Done(d) {
			return false
		}
	}
	return true
}

// Blockers returns the ids of the beads b is blocked by.
func Blockers(b Bead) []string {
	var ids []string
	for _, d := range b.Dependencies {
		if d.Type == "" || d.Type == "blocks" {
			ids = append(ids, d.DependsOn)
		}
	}
	return ids
}

// Stage returns the value of the bead's stage label, or "" if it has none.
// A bead carrying stage:parked and another stage label is being released by
// a human to that other stage (ADR-0010), so the other label wins; the
// scheduler then makes it the only one.
func (b Bead) Stage() string {
	st := ""
	for _, l := range b.Labels {
		if s, ok := strings.CutPrefix(l, StagePrefix); ok {
			if s != parked {
				return s
			}
			st = s
		}
	}
	return st
}

// parked is stage.Parked; plan does not import stage.
const parked = "parked"

// Source lists the beads of one plan.
type Source interface {
	// List returns every bead of the plan, open or closed, in plan order.
	List(ctx context.Context) ([]Bead, error)
}

// Store is a Source that can also move a bead. Stage labels live only in the
// local bd database; the export strips them (ADR-0005).
type Store interface {
	Source
	// SetStage makes "stage:<st>" the bead's only stage label.
	SetStage(ctx context.Context, id, st string) error
	// Close closes the bead with reason.
	Close(ctx context.Context, id, reason string) error
}

// BD is the Store backed by the bd CLI.
type BD struct {
	Dir    string // repository bd runs in
	Prefix string // plan prefix; beads "<Prefix>-<n>" belong to the plan

	// run executes bd with args in dir and returns its stdout. nil means exec.
	run func(ctx context.Context, dir string, args ...string) ([]byte, error)
}

// NewBD returns a Source that reads the plan with `bd list --json` in dir.
func NewBD(dir, prefix string) *BD {
	return &BD{Dir: dir, Prefix: prefix}
}

// List runs `bd list --json --all --limit 0` and keeps the plan's beads.
func (b *BD) List(ctx context.Context) ([]Bead, error) {
	out, err := b.runner()(ctx, b.Dir, "list", "--json", "--all", "--limit", "0")
	if err != nil {
		return nil, err
	}
	all, err := parseList(out)
	if err != nil {
		return nil, err
	}
	return Filter(all, b.Prefix), nil
}

// SetStage runs `bd label add` for the new stage label, then `bd label
// remove` for every other stage label. Both are skipped when already done, so
// a retry after a crash finishes the move.
func (b *BD) SetStage(ctx context.Context, id, st string) error {
	run := b.runner()
	beads, err := b.List(ctx)
	if err != nil {
		return err
	}
	var labels []string
	for _, x := range beads {
		if x.ID == id {
			labels = x.Labels
		}
	}
	want := StagePrefix + st
	if !slices.Contains(labels, want) {
		if _, err := run(ctx, b.Dir, "label", "add", id, want); err != nil {
			return err
		}
	}
	for _, l := range labels {
		if strings.HasPrefix(l, StagePrefix) && l != want {
			if _, err := run(ctx, b.Dir, "label", "remove", id, l); err != nil {
				return err
			}
		}
	}
	return nil
}

// Close runs `bd close <id> --reason <reason>`.
func (b *BD) Close(ctx context.Context, id, reason string) error {
	_, err := b.runner()(ctx, b.Dir, "close", id, "--reason", reason)
	return err
}

func (b *BD) runner() func(ctx context.Context, dir string, args ...string) ([]byte, error) {
	if b.run != nil {
		return b.run
	}
	return execBD
}

func execBD(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "bd", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("bd %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func parseList(out []byte) ([]Bead, error) {
	var beads []Bead
	if err := json.Unmarshal(out, &beads); err != nil {
		return nil, fmt.Errorf("parse bd list --json: %w", err)
	}
	for i := range beads {
		for j, l := range beads[i].Labels {
			beads[i].Labels[j] = strings.TrimSpace(l)
		}
	}
	return beads, nil
}

// Filter keeps the beads of plan prefix and sorts them in plan order.
func Filter(beads []Bead, prefix string) []Bead {
	var kept []Bead
	for _, b := range beads {
		if _, ok := stepNumber(b.ID, prefix); ok {
			kept = append(kept, b)
		}
	}
	slices.SortFunc(kept, func(x, y Bead) int {
		nx, _ := stepNumber(x.ID, prefix)
		ny, _ := stepNumber(y.ID, prefix)
		return nx - ny
	})
	return kept
}

// stepNumber returns n for an ID "<prefix>-<n>".
func stepNumber(id, prefix string) (int, bool) {
	rest, ok := strings.CutPrefix(id, prefix+"-")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(rest)
	return n, err == nil && n >= 0
}
