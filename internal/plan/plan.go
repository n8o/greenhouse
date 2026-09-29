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

// Bead is the part of a bd issue greenhouse uses.
type Bead struct {
	ID     string   `json:"id"`
	Title  string   `json:"title"`
	Status string   `json:"status"`
	Labels []string `json:"labels"`
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

// BD is the Source backed by the bd CLI.
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
	run := b.run
	if run == nil {
		run = execBD
	}
	out, err := run(ctx, b.Dir, "list", "--json", "--all", "--limit", "0")
	if err != nil {
		return nil, err
	}
	all, err := parseList(out)
	if err != nil {
		return nil, err
	}
	return Filter(all, b.Prefix), nil
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
