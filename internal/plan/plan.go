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

// Done reports whether the bead is finished: closed, and not still moving
// through the factory. An implement session closes its own bead inside its
// PR (ADR-0005), and in a shared bd database that close shows up before the
// PR merges; a stage label other than stage:done means it is not done yet.
func (b Bead) Done() bool {
	return b.Status == "closed" && (b.Stage() == "" || b.Stage() == "done")
}

// Ready reports whether b can start: it is open, and every bead it is blocked
// by is Done. A blocker missing from beads counts as open, so an unknown
// dependency holds the bead rather than releasing it.
func Ready(b Bead, beads []Bead) bool {
	if b.Status != "open" {
		return false
	}
	for _, d := range b.Dependencies {
		if d.Type != "" && d.Type != "blocks" {
			continue
		}
		i := slices.IndexFunc(beads, func(x Bead) bool { return x.ID == d.DependsOn })
		if i < 0 || !beads[i].Done() {
			return false
		}
	}
	return true
}

// Stage returns the value of the bead's stage label, or "" if it has none.
func (b Bead) Stage() string {
	for _, l := range b.Labels {
		if s, ok := strings.CutPrefix(l, StagePrefix); ok {
			return s
		}
	}
	return ""
}

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
