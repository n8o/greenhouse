package plan

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// Fake is an in-memory Store for tests. SetStage and Close change Beads, so
// a test can drive several ticks against it.
type Fake struct {
	Beads    []Bead
	Err      error
	WriteErr error    // returned by SetStage and Close
	Writes   []string // "stage <id> <st>" and "close <id>", in order
}

// List returns a copy of f.Beads, or f.Err if set.
func (f *Fake) List(context.Context) ([]Bead, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	out := make([]Bead, len(f.Beads))
	for i, b := range f.Beads {
		b.Labels = slices.Clone(b.Labels)
		out[i] = b
	}
	return out, nil
}

// SetStage implements Store.
func (f *Fake) SetStage(_ context.Context, id, st string) error {
	if f.WriteErr != nil {
		return f.WriteErr
	}
	b, err := f.find(id)
	if err != nil {
		return err
	}
	b.Labels = slices.DeleteFunc(b.Labels, func(l string) bool { return strings.HasPrefix(l, StagePrefix) })
	if st != "" {
		b.Labels = append(b.Labels, StagePrefix+st)
	}
	f.Writes = append(f.Writes, "stage "+id+" "+st)
	return nil
}

// Close implements Store.
func (f *Fake) Close(_ context.Context, id, _ string) error {
	if f.WriteErr != nil {
		return f.WriteErr
	}
	b, err := f.find(id)
	if err != nil {
		return err
	}
	b.Status = "closed"
	f.Writes = append(f.Writes, "close "+id)
	return nil
}

func (f *Fake) find(id string) (*Bead, error) {
	for i := range f.Beads {
		if f.Beads[i].ID == id {
			return &f.Beads[i], nil
		}
	}
	return nil, fmt.Errorf("fake plan: no bead %s", id)
}
