package gate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/n8o/greenhouse/internal/launch"
	"github.com/n8o/greenhouse/internal/stage"
)

// PRLabel identifies a greenhouse PR. The scheduler creates the label and
// puts it on every PR a stage session opens; the backlog gate counts open
// PRs carrying it, drafts included, since a draft is review work on its way.
// A label rather than a branch or title convention because it is one exact
// `gh pr list --label` filter and a human can see and change it on GitHub.
const PRLabel = "greenhouse"

// Probe gathers the gates' inputs. CLI shells out; tests use a Fake.
type Probe interface {
	// Usage returns Claude's cached usage reading, or nil if there is none.
	Usage(ctx context.Context) (*Reading, error)
	// ImplementSessions counts implement sessions still in flight.
	ImplementSessions(ctx context.Context) (int, error)
	// OpenPRs counts open greenhouse PRs.
	OpenPRs(ctx context.Context) (int, error)
}

// Gather fills Inputs from p. Counting errors are kept in Inputs, where the
// gates turn them into denials.
func Gather(ctx context.Context, p Probe, now time.Time) Inputs {
	in := Inputs{Now: now}
	in.Reading, in.ReadingErr = p.Usage(ctx)
	in.Implement, in.ImplementErr = p.ImplementSessions(ctx)
	in.OpenPRs, in.OpenPRsErr = p.OpenPRs(ctx)
	return in
}

// CLI is the Probe backed by agent-deck and gh. It only reads.
type CLI struct {
	Repo string // repository gh runs in
	// run executes name with args in dir and returns its stdout. nil means
	// exec.
	run func(ctx context.Context, dir, name string, args ...string) ([]byte, error)
}

// NewCLI returns a Probe that runs gh in repo.
func NewCLI(repo string) *CLI { return &CLI{Repo: repo} }

func (c *CLI) exec(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	if c.run != nil {
		return c.run(ctx, dir, name, args...)
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// Usage runs `agent-deck usage --json`.
func (c *CLI) Usage(ctx context.Context) (*Reading, error) {
	out, err := c.exec(ctx, "", "agent-deck", "usage", "--json")
	if err != nil {
		return nil, err
	}
	return parseUsage(out)
}

// usageJSON is `agent-deck usage --json`.
type usageJSON struct {
	Providers []struct {
		ID      string `json:"id"`
		Windows []struct {
			Kind           string  `json:"kind"`
			UsedPercentage float64 `json:"used_percentage"`
			ResetsAt       int64   `json:"resets_at"`
		} `json:"windows"`
		UpdatedAt int64 `json:"updated_at"`
		Stale     bool  `json:"stale"`
	} `json:"providers"`
}

func parseUsage(out []byte) (*Reading, error) {
	var u usageJSON
	if err := json.Unmarshal(out, &u); err != nil {
		return nil, fmt.Errorf("parse agent-deck usage --json: %w", err)
	}
	for _, p := range u.Providers {
		if p.ID != "claude" {
			continue
		}
		r := &Reading{Stale: p.Stale}
		if p.UpdatedAt > 0 {
			r.UpdatedAt = time.Unix(p.UpdatedAt, 0)
		}
		for _, w := range p.Windows {
			win := &Window{UsedPercentage: w.UsedPercentage}
			if w.ResetsAt > 0 {
				win.ResetsAt = time.Unix(w.ResetsAt, 0)
			}
			switch w.Kind {
			case "five_hour":
				r.FiveHour = win
			case "seven_day":
				r.SevenDay = win
			}
		}
		return r, nil
	}
	return nil, nil
}

// ImplementSessions runs `agent-deck list --json` and counts the sessions in
// the greenhouse group whose title names the implement stage and whose
// status is starting, running or waiting. A waiting session may be finished
// but not yet collected; it counts until the scheduler stops it, which errs
// toward fewer launches.
func (c *CLI) ImplementSessions(ctx context.Context) (int, error) {
	out, err := c.exec(ctx, "", "agent-deck", "list", "--json")
	if err != nil {
		return 0, err
	}
	return countImplement(out)
}

func countImplement(out []byte) (int, error) {
	var sessions []struct {
		Title  string `json:"title"`
		Group  string `json:"group"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(out, &sessions); err != nil {
		return 0, fmt.Errorf("parse agent-deck list --json: %w", err)
	}
	n := 0
	for _, s := range sessions {
		if s.Group != launch.Group && !strings.HasPrefix(s.Group, launch.Group+"/") {
			continue
		}
		if _, st, ok := launch.ParseTitle(s.Title); !ok || st != stage.Implement {
			continue
		}
		switch s.Status {
		case "starting", "running", "waiting":
			n++
		}
	}
	return n, nil
}

// OpenPRs runs `gh pr list --label greenhouse --state open` in Repo.
func (c *CLI) OpenPRs(ctx context.Context) (int, error) {
	out, err := c.exec(ctx, c.Repo, "gh", "pr", "list",
		"--label", PRLabel, "--state", "open", "--json", "number", "--limit", "1000")
	if err != nil {
		return 0, err
	}
	var prs []struct {
		Number int `json:"number"`
	}
	if err := json.Unmarshal(out, &prs); err != nil {
		return 0, fmt.Errorf("parse gh pr list --json: %w", err)
	}
	return len(prs), nil
}

// Fake is an in-memory Probe for tests.
type Fake struct {
	Reading      *Reading
	UsageErr     error
	Implement    int
	ImplementErr error
	Open         int
	OpenErr      error
}

func (f *Fake) Usage(context.Context) (*Reading, error) { return f.Reading, f.UsageErr }

func (f *Fake) ImplementSessions(context.Context) (int, error) { return f.Implement, f.ImplementErr }

func (f *Fake) OpenPRs(context.Context) (int, error) { return f.Open, f.OpenErr }
