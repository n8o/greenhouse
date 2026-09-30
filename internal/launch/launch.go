// Package launch starts stage sessions through agent-deck and reads their
// results (ADR-0003). It renders prompts/<stage>.md, launches the session
// with the stage's pinned model and effort, confirms the prompt actually
// reached Claude, and parses the session's final message into a stage.Event.
// Every agent-deck call goes through the Deck adapter.
package launch

import (
	"cmp"
	"context"
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
		Repo: l.Repo, Branch: req.Branch, NewBranch: true,
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

	ok, err = l.delivered(ctx, sess)
	if err != nil || ok {
		return run, err
	}
	run.Resent = true
	if err := l.Deck.Send(ctx, sess.ID, file); err != nil {
		return run, fmt.Errorf("launch %s: resend to %s: %w", args.Title, sess.ID, err)
	}
	if ok, err = l.delivered(ctx, sess); err != nil {
		return run, err
	}
	if !ok {
		return run, fmt.Errorf("launch %s: session %s: %w after one resend", args.Title, sess.ID, ErrNotDelivered)
	}
	return run, nil
}

// delivered polls until sess shows it received its prompt: it is working on
// a turn, or its own conversation has an assistant reply. agent-deck may
// report a launch as sent while Claude was not ready, and the message is then
// lost with the session idle at an empty prompt.
func (l *Launcher) delivered(ctx context.Context, sess Session) (bool, error) {
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
		if _, ok := l.ownOutput(ctx, sess.ID, cmp.Or(st.ClaudeSessionID, sess.ClaudeSessionID)); ok {
			return true, nil
		}
	}
	return false, nil
}

// ownOutput returns the session's last assistant message if it belongs to
// the session's own conversation. Before a session has a transcript,
// agent-deck can return another conversation's message from the same
// directory, which must never count as this session's.
func (l *Launcher) ownOutput(ctx context.Context, id, claudeID string) (string, bool) {
	out, err := l.Deck.Output(ctx, id)
	if err != nil || claudeID == "" || out.ClaudeSessionID != claudeID || strings.TrimSpace(out.Content) == "" {
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
	state, err := l.Deck.Show(ctx, id)
	if err != nil {
		return stage.Event{}, false, err
	}
	if state.Busy() {
		return stage.Event{}, false, nil
	}
	if state.Blocked() {
		return stage.Event{}, true, fmt.Errorf("%w: session %s", ErrBlocked, id)
	}
	content, ok := l.ownOutput(ctx, id, state.ClaudeSessionID)
	if !ok {
		return stage.Event{}, true, fmt.Errorf("%w: session %s is %s with no reply of its own", ErrNoResult, id, state.Status)
	}
	ev, err = ParseResult(st, content)
	return ev, true, err
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
