// Package stage is the pure state machine that moves a bead through the
// factory: Next(stage, event) → (next stage, actions). It does no I/O, reads
// no clock and uses no randomness (ADR-0001). Everything a transition depends
// on, the retry count included, arrives in the Event, and the Actions it
// returns are data the scheduler carries out.
//
// It is also the single source of stage names; other packages use its
// constants rather than string literals.
package stage

import (
	"errors"
	"fmt"
)

// Stage is where a bead is in the pipeline. It is stored as the bead's
// "stage:<name>" label.
type Stage string

// The stages of docs/DESIGN.md, in pipeline order.
const (
	Spec      Stage = "spec"      // LLM: acceptance criteria + file plan
	Implement Stage = "implement" // LLM: commits and a draft PR
	Verify    Stage = "verify"    // deterministic checks on the PR branch
	Review    Stage = "review"    // the human reviews the ready PR
	Parked    Stage = "parked"    // waiting on a human decision
	Learn     Stage = "learn"     // LLM: lesson from a rejected PR
	Done      Stage = "done"      // merged; the bead is closed
)

// All returns every stage in pipeline order.
func All() []Stage {
	return []Stage{Spec, Implement, Verify, Review, Parked, Learn, Done}
}

// Valid reports whether s is one of All.
func (s Stage) Valid() bool {
	switch s {
	case Spec, Implement, Verify, Review, Parked, Learn, Done:
		return true
	}
	return false
}

// MaxVerifyRetries is how many times a verify failure sends the bead back to
// implement before it is parked (ADR-0004: retry once, then park).
const MaxVerifyRetries = 1

// EventKind is what happened to a bead in its current stage.
type EventKind string

const (
	SpecDone        EventKind = "spec-done"        // spec session finished
	SpecVague       EventKind = "spec-vague"       // bead too vague to spec; needs a human
	ImplementDone   EventKind = "implement-done"   // implement session opened a draft PR
	ImplementFailed EventKind = "implement-failed" // implement session ended without a PR
	VerifyPass      EventKind = "verify-pass"      // every verify command passed
	VerifyFail      EventKind = "verify-fail"      // a verify command failed
	Merged          EventKind = "merged"           // the human merged the PR
	Rejected        EventKind = "rejected"         // the human closed the PR unmerged
	LearnDone       EventKind = "learn-done"       // learn session proposed its lesson
	Unparked        EventKind = "unparked"         // the human released a parked bead
	NeedsHuman      EventKind = "needs-human"      // a stage session asked a question only a human can answer
)

// EventKinds returns every event kind.
func EventKinds() []EventKind {
	return []EventKind{SpecDone, SpecVague, ImplementDone, ImplementFailed, VerifyPass,
		VerifyFail, Merged, Rejected, LearnDone, Unparked, NeedsHuman}
}

// Event is an input to Next.
type Event struct {
	Kind EventKind
	// Reason is the human-readable why: the vague-spec note, the failure or
	// verify log, the rejection reason, or the question a session needs
	// answered. Required by every event that parks, retries or learns.
	Reason string
	// PR is the pull request number. Required by ImplementDone and
	// VerifyPass.
	PR int
	// Retries is how many verify failures sent this bead back to implement
	// since its last spec run. Read by VerifyFail; the scheduler derives it
	// from the Record actions it has carried out.
	Retries int
}

// ActionKind is an effect the scheduler performs after a transition.
type ActionKind string

const (
	ActLaunch    ActionKind = "launch"     // start a session for Action.Stage, with Action.Reason as context
	ActVerify    ActionKind = "verify"     // run the verify commands on Action.PR (no LLM)
	ActMarkReady ActionKind = "mark-ready" // gh pr ready Action.PR: validated, review me
	ActPark      ActionKind = "park"       // label the bead parked, with Action.Reason for the human
	ActCloseBead ActionKind = "close-bead" // close the bead
	ActRecord    ActionKind = "record"     // record Action.Reason on the bead so it survives a restart
)

// Action is one effect, as data. Only the fields its Kind names are set.
type Action struct {
	Kind   ActionKind
	Stage  Stage
	PR     int
	Reason string
}

// ErrInvalid is wrapped by every error Next returns.
var ErrInvalid = errors.New("invalid transition")

// Start is the transition of a newly picked approved bead that has no stage
// yet: it goes to spec.
func Start() (Stage, []Action) {
	return Spec, []Action{{Kind: ActLaunch, Stage: Spec}}
}

// Next returns the stage a bead in stage s moves to on event e, and the
// actions that carry the move out. An unknown stage or event, a pair with no
// transition, or an event missing a field the transition needs is an error
// wrapping ErrInvalid; the bead then stays where it is.
func Next(s Stage, e Event) (Stage, []Action, error) {
	if !s.Valid() {
		return s, nil, fmt.Errorf("%w: unknown stage %q", ErrInvalid, s)
	}
	fail := func(format string, args ...any) (Stage, []Action, error) {
		return s, nil, fmt.Errorf("%w: %s on %s: %s", ErrInvalid, s, e.Kind, fmt.Sprintf(format, args...))
	}
	switch {
	case s == Spec && e.Kind == SpecDone:
		return Implement, []Action{{Kind: ActLaunch, Stage: Implement}}, nil

	case s == Spec && e.Kind == SpecVague,
		(s == Spec || s == Implement || s == Learn) && e.Kind == NeedsHuman,
		s == Implement && e.Kind == ImplementFailed:
		if e.Reason == "" {
			return fail("reason is required")
		}
		return Parked, []Action{{Kind: ActPark, Reason: e.Reason}}, nil

	case s == Implement && e.Kind == ImplementDone:
		if e.PR <= 0 {
			return fail("PR number is required, got %d", e.PR)
		}
		return Verify, []Action{{Kind: ActVerify, PR: e.PR}}, nil

	case s == Verify && e.Kind == VerifyPass:
		if e.PR <= 0 {
			return fail("PR number is required, got %d", e.PR)
		}
		return Review, []Action{{Kind: ActMarkReady, PR: e.PR}}, nil

	case s == Verify && e.Kind == VerifyFail:
		if e.Reason == "" {
			return fail("reason (the verify log) is required")
		}
		if e.Retries < 0 {
			return fail("retries must be >= 0, got %d", e.Retries)
		}
		record := Action{Kind: ActRecord, Reason: e.Reason}
		if e.Retries < MaxVerifyRetries {
			return Implement, []Action{record, {Kind: ActLaunch, Stage: Implement, Reason: e.Reason}}, nil
		}
		return Parked, []Action{record, {Kind: ActPark,
			Reason: fmt.Sprintf("verify failed with %d/%d retries used: %s", e.Retries, MaxVerifyRetries, e.Reason)}}, nil

	case s == Review && e.Kind == Merged:
		return Done, []Action{{Kind: ActCloseBead}}, nil

	case s == Review && e.Kind == Rejected:
		if e.Reason == "" {
			return fail("reason (why the PR was rejected) is required")
		}
		return Learn, []Action{{Kind: ActRecord, Reason: e.Reason}, {Kind: ActLaunch, Stage: Learn, Reason: e.Reason}}, nil

	case s == Learn && e.Kind == LearnDone:
		// A rejected bead's "Done when" does not hold, so it is not closed:
		// the human re-plans it and unparks it, or closes it.
		return Parked, []Action{{Kind: ActPark, Reason: "PR rejected and lesson proposed: re-plan and unpark, or close the bead"}}, nil

	case s == Parked && e.Kind == Unparked:
		return Spec, []Action{{Kind: ActLaunch, Stage: Spec}}, nil
	}

	for _, k := range EventKinds() {
		if e.Kind == k {
			return fail("no such transition")
		}
	}
	return s, nil, fmt.Errorf("%w: unknown event %q", ErrInvalid, e.Kind)
}
