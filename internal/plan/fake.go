package plan

import "context"

// Fake is an in-memory Source for tests.
type Fake struct {
	Beads []Bead
	Err   error
}

// List returns f.Beads, or f.Err if set.
func (f *Fake) List(context.Context) ([]Bead, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	return f.Beads, nil
}
