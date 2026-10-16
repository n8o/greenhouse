// Package launch starts stage sessions through agent-deck and reads their
// results (ADR-0003). It renders prompts/<stage>.md, launches the session
// with the stage's pinned model and effort, confirms the prompt actually
// reached Claude, and parses the session's final message into a stage.Event.
// Every agent-deck call goes through the Deck adapter.
package launch

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/n8o/greenhouse/internal/config"
	"github.com/n8o/greenhouse/internal/stage"
)

// Group is the agent-deck group every stage session is created in.
const Group = "greenhouse"

// ErrNotDelivered is wrapped when a launched session never received its
// prompt, even after one resend.
var ErrNotDelivered = errors.New("prompt not delivered")

// ErrBlocked is wrapped when a session is stuck on an interactive prompt
// (a permission or first-run dialog). Nobody will answer it; a human must
// attach.
var ErrBlocked = errors.New("session blocked on an interactive prompt")

// Launcher starts stage sessions for one repo.
type Launcher struct {
	Deck      Deck
	Repo      string
	PromptDir string                  // default <Repo>/prompts
	Stages    map[string]config.Stage // model, effort and turn budget per stage
	// Parent is the agent-deck session stage sessions report to; "" starts
	// them without one.
	Parent string

	// Delivery is confirmed by polling the session every PollInterval, up
	// to DeliveryPolls times, before resending once.
	PollInterval  time.Duration
	DeliveryPolls int
	sleep         func(time.Duration) // nil means time.Sleep
}

// New returns a Launcher for cfg's repo and stage budgets.
func New(cfg config.Config, deck Deck) *Launcher {
	return &Launcher{
		Deck:          deck,
		Repo:          cfg.Repo,
		PromptDir:     filepath.Join(cfg.Repo, "prompts"),
		Stages:        cfg.Stages,
		PollInterval:  3 * time.Second,
		DeliveryPolls: 40,
	}
}

// Run is a launched stage session.
type Run struct {
	Session
	Bead   string
	Stage  stage.Stage
	Branch string
	Resent bool // the first delivery was lost and the prompt was sent again

	// Set by Resume only.
	Reused    bool // continues an existing session instead of a new one
	Restarted bool // the session was stopped and was started again
	Sent      bool // the retry prompt was typed into the session
	Adopted   bool // the session was already working; nothing was sent
}

// Title is the agent-deck title of bead's stage-st session. ParseTitle
// reverses it, so the scheduler can map a session back to its bead.
func Title(bead string, st stage.Stage) string { return "gh-" + string(st) + "-" + bead }

// ParseTitle returns the bead and stage of a Title, or ok false.
func ParseTitle(title string) (bead string, st stage.Stage, ok bool) {
	rest, ok := strings.CutPrefix(title, "gh-")
	if !ok {
		return "", "", false
	}
	name, bead, ok := strings.Cut(rest, "-")
	st = stage.Stage(name)
	if !ok || bead == "" || !st.Valid() {
		return "", "", false
	}
	return bead, st, true
}

// Launch renders req's prompt and starts its session with the stage's pinned
// model and effort. It then confirms the session received the prompt and
// resends it once if not; a prompt still undelivered is an error wrapping
// ErrNotDelivered. The session is left running either way, so a human can
// attach to it.
func (l *Launcher) Launch(ctx context.Context, req Request) (Run, error) {
	budget, ok := l.Stages[string(req.Stage)]
	if !ok {
		return Run{}, fmt.Errorf("launch %s %s: no stage budget configured", req.Bead, req.Stage)
	}
	if req.Branch == "" {
		req.Branch = DefaultBranch(req.Bead, req.Stage)
	}
	prompt, err := Render(l.PromptDir, req, budget.MaxTurns)
	if err != nil {
		return Run{}, err
	}
	file, err := writeTemp(prompt)
	if err != nil {
		return Run{}, err
	}
	defer os.Remove(file)

	args := LaunchArgs{
		Repo: l.Repo, Branch: req.Branch, NewBranch: !req.ExistingBranch,
		Title: Title(req.Bead, req.Stage), Group: Group, Parent: l.Parent,
		Model: budget.Model, Effort: budget.Effort,
		MessageFile: file,
	}
	sess, err := l.Deck.Launch(ctx, args)
	if err != nil {
		return Run{}, fmt.Errorf("launch %s: %w", args.Title, err)
	}
	run := Run{Session: sess, Bead: req.Bead, Stage: req.Stage, Branch: req.Branch}
	if sess.Model != budget.Model || sess.Effort != budget.Effort {
		return run, fmt.Errorf("launch %s: session %s runs model %q effort %q, want the pinned %q %q",
			args.Title, sess.ID, sess.Model, sess.Effort, budget.Model, budget.Effort)
	}

	run.Resent, err = l.confirm(ctx, "launch "+args.Title, sess, file, Prior{})
	return run, err
}

// confirm checks that sess received the message in file and resends it once
// if not. Only a reply after prior counts as delivery. resent reports the
// resend.
func (l *Launcher) confirm(ctx context.Context, what string, sess Session, file string, prior Prior) (resent bool, err error) {
	ok, err := l.delivered(ctx, sess, prior)
	if err != nil || ok {
		return false, err
	}
	if err := l.Deck.Send(ctx, sess.ID, file); err != nil {
		return true, fmt.Errorf("%s: resend to %s: %w", what, sess.ID, err)
	}
	if ok, err = l.delivered(ctx, sess, prior); err != nil {
		return true, err
	}
	if !ok {
		return true, fmt.Errorf("%s: session %s: %w after one resend", what, sess.ID, ErrNotDelivered)
	}
	return true, nil
}

// Prior marks the end of a reused session's previous attempt: its last
// assistant message when the retry was decided. That message (the previous
// attempt's completion) must never be read as the retry's. The zero Prior
// is a session's first attempt, where any reply of its own counts.
type Prior struct {
	At     time.Time // the message's transcript timestamp; zero if unknown
	Digest string    // sha256 of its content; "" if there was none
}

// PriorOf is the Prior that o ends.
func PriorOf(o Output) Prior {
	if strings.TrimSpace(o.Content) == "" {
		return Prior{}
	}
	return Prior{At: o.At(), Digest: digest(o.Content)}
}

// IsZero reports whether p marks no previous attempt.
func (p Prior) IsZero() bool { return p.At.IsZero() && p.Digest == "" }

// Fresh reports whether o was written after p. Both timestamps come from
// the same transcript, so no clock is compared with the scheduler's. With
// a timestamp missing, a message differing from the previous one is new.
func (p Prior) Fresh(o Output) bool {
	if p.IsZero() {
		return true
	}
	if at := o.At(); !at.IsZero() && !p.At.IsZero() {
		return at.After(p.At)
	}
	return digest(o.Content) != p.Digest
}

func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// Existing is a stage session still registered in agent-deck: its title is
// taken, so a retry of its stage continues it rather than launching anew.
type Existing struct {
	Listed
	Prior Prior // where its previous attempt ended
}

// Find returns the registered session of bead's stage-st run, if any, with
// the Prior its retry must look past. agent-deck refuses a second session
// with the same Title, and the previous attempt's context and worktree are
// worth keeping, so a found session is resumed, never replaced.
func (l *Launcher) Find(ctx context.Context, bead string, st stage.Stage) (Existing, bool, error) {
	s, ok, err := l.Deck.Find(ctx, Title(bead, st))
	if err != nil || !ok {
		return Existing{}, false, err
	}
	ex := Existing{Listed: s}
	out, err := l.Deck.Output(ctx, s.ID)
	if err != nil {
		// Without the baseline the previous completion could pass for the
		// retry's; do not guess.
		return Existing{}, false, fmt.Errorf("find %s: read its last reply: %w", s.Title, err)
	}
	if s.ClaudeSessionID == "" || out.ClaudeSessionID == s.ClaudeSessionID {
		ex.Prior = PriorOf(out)
	}
	return ex, true, nil
}

// Resume continues ex for req: it starts ex again if it is stopped, sends
// it prompts/retry.md, and confirms delivery the way Launch does. A session
// already working was resumed before (by a tick that crashed before
// recording it, or by a human) and is adopted without a send.
func (l *Launcher) Resume(ctx context.Context, req Request, ex Existing) (Run, error) {
	budget, ok := l.Stages[string(req.Stage)]
	if !ok {
		return Run{}, fmt.Errorf("resume %s %s: no stage budget configured", req.Bead, req.Stage)
	}
	if req.Branch == "" {
		req.Branch = DefaultBranch(req.Bead, req.Stage)
	}
	sess := Session{ID: ex.ID, Title: ex.Title, ClaudeSessionID: ex.ClaudeSessionID, Path: ex.Path, Status: ex.Status}
	run := Run{Session: sess, Bead: req.Bead, Stage: req.Stage, Branch: req.Branch, Reused: true}
	if busy(ex.Status) {
		run.Adopted = true
		return run, nil
	}
	prompt, err := RenderRetry(l.PromptDir, req, budget.MaxTurns)
	if err != nil {
		return Run{}, err
	}
	file, err := writeTemp(prompt)
	if err != nil {
		return Run{}, err
	}
	defer os.Remove(file)

	what := "resume " + ex.Title
	if ex.Status == "stopped" || ex.Status == "error" {
		if err := l.Deck.Start(ctx, ex.ID); err != nil {
			return Run{}, fmt.Errorf("%s: start %s: %w", what, ex.ID, err)
		}
		run.Restarted = true
	}
	if err := l.Deck.Send(ctx, ex.ID, file); err != nil {
		return run, fmt.Errorf("%s: send to %s: %w", what, ex.ID, err)
	}
	run.Sent = true
	run.Resent, err = l.confirm(ctx, what, sess, file, ex.Prior)
	return run, err
}

// delivered polls until sess shows it received its prompt: it is working on
// a turn, or its own conversation has an assistant reply after prior.
// agent-deck may report a launch as sent while Claude was not ready, and the
// message is then lost with the session idle at an empty prompt.
func (l *Launcher) delivered(ctx context.Context, sess Session, prior Prior) (bool, error) {
	for i := range l.DeliveryPolls {
		if i > 0 {
			l.wait(ctx)
		}
		if err := ctx.Err(); err != nil {
			return false, err
		}
		st, err := l.Deck.Show(ctx, sess.ID)
		if err != nil {
			return false, fmt.Errorf("confirm delivery to %s: %w", sess.ID, err)
		}
		switch st.Status {
		case "running":
			return true, nil
		case "error", "stopped":
			return false, fmt.Errorf("confirm delivery to %s: session is %s", sess.ID, st.Status)
		}
		if st.Status == "starting" {
			continue
		}
		if _, ok := l.ownOutput(ctx, sess.ID, cmp.Or(st.ClaudeSessionID, sess.ClaudeSessionID), prior); ok {
			return true, nil
		}
	}
	return false, nil
}

// ownOutput returns the session's last assistant message if it belongs to
// the session's own conversation and was written after prior. Before a
// session has a transcript, agent-deck can return another conversation's
// message from the same directory, which must never count as this
// session's; nor may a resumed session's previous completion.
func (l *Launcher) ownOutput(ctx context.Context, id, claudeID string, prior Prior) (string, bool) {
	out, err := l.Deck.Output(ctx, id)
	if err != nil || claudeID == "" || out.ClaudeSessionID != claudeID || strings.TrimSpace(out.Content) == "" || !prior.Fresh(out) {
		return "", false
	}
	return out.Content, true
}

// Result reads the outcome of the stage-st session id. done is false while
// the session is still starting or working. A session stuck on an
// interactive prompt is done with an error wrapping ErrBlocked. Otherwise its
// final message must follow the result contract, or Result returns an error
// wrapping ErrNoResult rather than a guessed event.
func (l *Launcher) Result(ctx context.Context, id string, st stage.Stage) (ev stage.Event, done bool, err error) {
	ev, _, done, err = l.Collect(ctx, id, st, Prior{})
	return ev, done, err
}

// Collect is Result that also returns the session's final message, so the
// caller can look for a usage-limit stop in it whatever the event. prior is
// where a resumed session's previous attempt ended: until the session has
// replied after it, the run is not done, whatever its status, since the
// last message is still the previous attempt's completion. The stage's
// wall-clock limit bounds that wait.
func (l *Launcher) Collect(ctx context.Context, id string, st stage.Stage, prior Prior) (ev stage.Event, content string, done bool, err error) {
	state, err := l.Deck.Show(ctx, id)
	if err != nil {
		return stage.Event{}, "", false, err
	}
	if state.Busy() {
		return stage.Event{}, "", false, nil
	}
	if state.Blocked() {
		return stage.Event{}, "", true, fmt.Errorf("%w: session %s", ErrBlocked, id)
	}
	content, ok := l.ownOutput(ctx, id, state.ClaudeSessionID, prior)
	if !ok && !prior.IsZero() {
		return stage.Event{}, "", false, nil
	}
	if !ok {
		return stage.Event{}, "", true, fmt.Errorf("%w: session %s is %s with no reply of its own", ErrNoResult, id, state.Status)
	}
	ev, err = ParseResult(st, content)
	return ev, content, true, err
}

// Completion is the outcome of one finished stage session.
type Completion struct {
	SessionID string
	Bead      string
	Stage     stage.Stage
	Event     stage.Event
	Err       error // the session broke the result contract
}

// Completions returns the outcome of every finished greenhouse session under
// Parent, read from `agent-deck session children`. Sessions still working,
// and children whose title is not a greenhouse Title, are left out.
func (l *Launcher) Completions(ctx context.Context) ([]Completion, error) {
	if l.Parent == "" {
		return nil, errors.New("completions: no parent session configured")
	}
	kids, err := l.Deck.Children(ctx, l.Parent)
	if err != nil {
		return nil, err
	}
	var done []Completion
	for _, k := range kids {
		bead, st, ok := ParseTitle(k.Title)
		if !ok || busy(k.Status) {
			continue
		}
		ev, finished, err := l.Result(ctx, k.ID, st)
		if !finished && err == nil {
			continue
		}
		done = append(done, Completion{SessionID: k.ID, Bead: bead, Stage: st, Event: ev, Err: err})
	}
	return done, nil
}

func (l *Launcher) wait(ctx context.Context) {
	if l.sleep != nil {
		l.sleep(l.PollInterval)
		return
	}
	select {
	case <-ctx.Done():
	case <-time.After(l.PollInterval):
	}
}

func writeTemp(prompt string) (string, error) {
	f, err := os.CreateTemp("", "greenhouse-prompt-*.md")
	if err != nil {
		return "", err
	}
	if _, err := f.WriteString(prompt); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), f.Close()
}
