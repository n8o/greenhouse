package launch

import (
	"context"
	"fmt"
	"os"
)

// FakeDeck is an in-memory Deck for tests. Show and Output pop the next
// scripted value for a session each call and repeat the last one once the
// script runs out.
type FakeDeck struct {
	Launched    []LaunchArgs
	Prompts     []string // message file contents, launch then sends, in order
	Sends       []string // session ids sent to
	LaunchErr   error
	SendErr     error
	NextSession Session // returned by Launch; ID defaults to "s1"

	States   map[string][]State
	Outputs  map[string][]Output
	Kids     map[string][]Child
	ShowErr  error
	OutErr   error
	ChildErr error
}

// Launch records a and returns NextSession, echoing the pinned model and
// effort unless NextSession sets them.
func (f *FakeDeck) Launch(_ context.Context, a LaunchArgs) (Session, error) {
	f.Launched = append(f.Launched, a)
	if err := f.readPrompt(a.MessageFile); err != nil {
		return Session{}, err
	}
	if f.LaunchErr != nil {
		return Session{}, f.LaunchErr
	}
	s := f.NextSession
	if s.ID == "" {
		s.ID = "s1"
	}
	if s.Title == "" {
		s.Title = a.Title
	}
	if s.Model == "" {
		s.Model = a.Model
	}
	if s.Effort == "" {
		s.Effort = a.Effort
	}
	return s, nil
}

func (f *FakeDeck) readPrompt(file string) error {
	b, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("fake deck: %w", err)
	}
	f.Prompts = append(f.Prompts, string(b))
	return nil
}

// Show returns the next scripted State of id.
func (f *FakeDeck) Show(_ context.Context, id string) (State, error) {
	if f.ShowErr != nil {
		return State{}, f.ShowErr
	}
	s, ok := pop(f.States, id)
	if !ok {
		return State{}, fmt.Errorf("fake deck: no state for %s", id)
	}
	return s, nil
}

// Output returns the next scripted Output of id.
func (f *FakeDeck) Output(_ context.Context, id string) (Output, error) {
	if f.OutErr != nil {
		return Output{}, f.OutErr
	}
	o, ok := pop(f.Outputs, id)
	if !ok {
		return Output{}, fmt.Errorf("fake deck: no output for %s", id)
	}
	return o, nil
}

// Send records the message sent to id.
func (f *FakeDeck) Send(_ context.Context, id, messageFile string) error {
	f.Sends = append(f.Sends, id)
	if err := f.readPrompt(messageFile); err != nil {
		return err
	}
	return f.SendErr
}

// Children returns Kids[parent].
func (f *FakeDeck) Children(_ context.Context, parent string) ([]Child, error) {
	if f.ChildErr != nil {
		return nil, f.ChildErr
	}
	return f.Kids[parent], nil
}

func pop[T any](m map[string][]T, id string) (T, bool) {
	var zero T
	q := m[id]
	if len(q) == 0 {
		return zero, false
	}
	v := q[0]
	if len(q) > 1 {
		m[id] = q[1:]
	}
	return v, true
}
