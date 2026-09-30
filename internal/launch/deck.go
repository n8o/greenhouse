package launch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Deck is the agent-deck CLI (ADR-0003). It is the only way greenhouse starts
// or observes a session; callers depend on it so tests can swap in a
// FakeDeck.
type Deck interface {
	// Launch creates and starts a session and hands it its first message.
	Launch(ctx context.Context, a LaunchArgs) (Session, error)
	// Show returns a session's live state.
	Show(ctx context.Context, id string) (State, error)
	// Output returns the last assistant message agent-deck can find for a
	// session. Before the session has a transcript it may return another
	// conversation's message, so callers match ClaudeSessionID.
	Output(ctx context.Context, id string) (Output, error)
	// Send types the message in file into a running session.
	Send(ctx context.Context, id, messageFile string) error
	// Children lists the sub-sessions of parent with their live status.
	Children(ctx context.Context, parent string) ([]Child, error)
}

// LaunchArgs is one `agent-deck launch` call.
type LaunchArgs struct {
	Repo      string // project directory
	Branch    string // worktree branch
	NewBranch bool   // create Branch (-b)
	Title     string
	Group     string
	Parent    string // parent session; "" launches with -no-parent
	Model     string
	Effort    string
	// MessageFile holds the rendered prompt. agent-deck appends its
	// completion-sentinel instruction (-assert-done).
	MessageFile string
}

// Args returns the agent-deck argument list for a.
func (a LaunchArgs) Args() []string {
	args := []string{"launch", a.Repo, "-json",
		"-t", a.Title, "-c", "claude", "-g", a.Group,
		"-worktree", a.Branch}
	if a.NewBranch {
		args = append(args, "-b")
	}
	if a.Parent != "" {
		args = append(args, "-p", a.Parent)
	} else {
		// A session inside agent-deck would otherwise become the parent,
		// and agent-deck allows only one level of sub-sessions.
		args = append(args, "-no-parent")
	}
	return append(args,
		"-model", a.Model, "-effort", a.Effort,
		// An unattended session stalls on any permission prompt.
		"-auto-mode",
		"-assert-done",
		"-message-file", a.MessageFile)
}

// Session is what `agent-deck launch -json` reports about a new session.
type Session struct {
	ID              string `json:"id"`
	Title           string `json:"title"`
	ClaudeSessionID string `json:"claude_session_id"`
	Model           string `json:"model"`
	Effort          string `json:"effort"`
	Path            string `json:"path"`
	Status          string `json:"status"`
	// MessagePending is true when agent-deck has not yet typed the message.
	MessagePending bool `json:"message_pending"`
}

// State is the part of `agent-deck session show -json` greenhouse reads.
type State struct {
	ID              string `json:"id"`
	Status          string `json:"status"`   // starting, running, waiting, idle, error, stopped
	Substate        string `json:"substate"` // e.g. idle-at-empty-prompt, interactive-menu
	ClaudeSessionID string `json:"claude_session_id"`
}

// Busy reports whether the session is still starting or working on a turn.
func (s State) Busy() bool { return busy(s.Status) }

// Blocked reports whether the session is waiting on an interactive prompt,
// such as a permission dialog, rather than idle at an empty prompt.
func (s State) Blocked() bool { return s.Status == "waiting" && s.Substate == "interactive-menu" }

func busy(status string) bool { return status == "starting" || status == "running" }

// Output is `agent-deck session output -json`.
type Output struct {
	ClaudeSessionID string `json:"claude_session_id"`
	Content         string `json:"content"`
}

// Child is one entry of `agent-deck session children -json`.
type Child struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

// CLI is the Deck backed by the agent-deck binary.
type CLI struct {
	// run executes agent-deck with args and returns its stdout, which is
	// returned even when the command fails. nil means exec.
	run func(ctx context.Context, args ...string) ([]byte, error)
}

// NewCLI returns a Deck that shells out to agent-deck on PATH.
func NewCLI() *CLI { return &CLI{} }

func (c *CLI) exec(ctx context.Context, args ...string) ([]byte, error) {
	if c.run != nil {
		return c.run(ctx, args...)
	}
	cmd := exec.CommandContext(ctx, "agent-deck", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("agent-deck %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// call runs agent-deck and decodes its JSON stdout into v. agent-deck reports
// a failure as {"success": false, "error": "..."} on stdout, so that message
// is preferred over the bare exit status.
func (c *CLI) call(ctx context.Context, v any, args ...string) error {
	out, runErr := c.exec(ctx, args...)
	var failure struct {
		Success *bool  `json:"success"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(out, &failure) == nil && failure.Success != nil && !*failure.Success {
		return fmt.Errorf("agent-deck %s: %s", args[0], failure.Error)
	}
	if runErr != nil {
		return runErr
	}
	if v == nil {
		return nil
	}
	if err := json.Unmarshal(out, v); err != nil {
		return fmt.Errorf("parse agent-deck %s -json: %w", strings.Join(args[:min(2, len(args))], " "), err)
	}
	return nil
}

// Launch runs `agent-deck launch ... -json`.
func (c *CLI) Launch(ctx context.Context, a LaunchArgs) (Session, error) {
	var s Session
	if err := c.call(ctx, &s, a.Args()...); err != nil {
		return Session{}, err
	}
	if s.ID == "" {
		return Session{}, errors.New("agent-deck launch -json: no session id")
	}
	return s, nil
}

// Show runs `agent-deck session show <id> -json`.
func (c *CLI) Show(ctx context.Context, id string) (State, error) {
	var s State
	err := c.call(ctx, &s, "session", "show", id, "-json")
	return s, err
}

// Output runs `agent-deck session output <id> -json`.
func (c *CLI) Output(ctx context.Context, id string) (Output, error) {
	var o Output
	err := c.call(ctx, &o, "session", "output", id, "-json")
	return o, err
}

// Send runs `agent-deck session send <id> -message-file <file>`. It waits
// for the session to be ready before typing.
func (c *CLI) Send(ctx context.Context, id, messageFile string) error {
	_, err := c.exec(ctx, "session", "send", id, "-message-file", messageFile, "-q")
	return err
}

// Children runs `agent-deck session children <parent> -json`.
func (c *CLI) Children(ctx context.Context, parent string) ([]Child, error) {
	var r struct {
		Children []Child `json:"children"`
	}
	err := c.call(ctx, &r, "session", "children", parent, "-json")
	return r.Children, err
}
