package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"slices"
	"strconv"
	"strings"

	"github.com/n8o/greenhouse/internal/gate"
)

// GitHub is the gh CLI as the scheduler uses it. Every write is safe to
// repeat, so a tick that crashed halfway can redo it.
type GitHub interface {
	// EnsureLabel creates the gate.PRLabel label if the repo lacks it.
	EnsureLabel(ctx context.Context) error
	// PR returns pull request n.
	PR(ctx context.Context, n int) (PR, error)
	// AddLabel puts gate.PRLabel on PR n.
	AddLabel(ctx context.Context, n int) error
	// Ready marks draft PR n ready for review.
	Ready(ctx context.Context, n int) error
}

// PR is the part of `gh pr view --json` the scheduler reads.
type PR struct {
	Number  int
	State   string // OPEN, MERGED or CLOSED
	IsDraft bool
	HeadRef string
	Labels  []string
	// Reason is the last review's body, the human's why for a closed PR;
	// "" if there is none.
	Reason string
}

type ghLabel struct {
	Name string `json:"name"`
}

// ghCLI is the GitHub backed by gh, run in Repo.
type ghCLI struct {
	Repo string
	// run executes gh with args and returns its stdout. nil means exec.
	run func(ctx context.Context, args ...string) ([]byte, error)
}

func (g *ghCLI) gh(ctx context.Context, args ...string) ([]byte, error) {
	if g.run != nil {
		return g.run(ctx, args...)
	}
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = g.Repo
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gh %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func (g *ghCLI) EnsureLabel(ctx context.Context) error {
	out, err := g.gh(ctx, "label", "list", "--search", gate.PRLabel, "--json", "name", "--limit", "100")
	if err != nil {
		return err
	}
	// gh prints nothing at all, not [], when --search matches no label.
	var labels []ghLabel
	if len(bytes.TrimSpace(out)) == 0 {
		out = []byte("[]")
	}
	if err := json.Unmarshal(out, &labels); err != nil {
		return fmt.Errorf("parse gh label list --json: %w", err)
	}
	if slices.Contains(labels, ghLabel{gate.PRLabel}) {
		return nil
	}
	_, err = g.gh(ctx, "label", "create", gate.PRLabel, "--color", "2E7D32",
		"--description", "Opened by a greenhouse stage session")
	return err
}

func (g *ghCLI) PR(ctx context.Context, n int) (PR, error) {
	out, err := g.gh(ctx, "pr", "view", strconv.Itoa(n), "--json", "number,state,isDraft,headRefName,labels,reviews")
	if err != nil {
		return PR{}, err
	}
	return parsePR(out)
}

func parsePR(out []byte) (PR, error) {
	var v struct {
		Number      int       `json:"number"`
		State       string    `json:"state"`
		IsDraft     bool      `json:"isDraft"`
		HeadRefName string    `json:"headRefName"`
		Labels      []ghLabel `json:"labels"`
		Reviews     []struct {
			Body string `json:"body"`
		} `json:"reviews"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return PR{}, fmt.Errorf("parse gh pr view --json: %w", err)
	}
	pr := PR{Number: v.Number, State: v.State, IsDraft: v.IsDraft, HeadRef: v.HeadRefName}
	for _, l := range v.Labels {
		pr.Labels = append(pr.Labels, l.Name)
	}
	for _, r := range v.Reviews {
		if b := strings.TrimSpace(r.Body); b != "" {
			pr.Reason = b
		}
	}
	return pr, nil
}

func (g *ghCLI) AddLabel(ctx context.Context, n int) error {
	_, err := g.gh(ctx, "pr", "edit", strconv.Itoa(n), "--add-label", gate.PRLabel)
	return err
}

func (g *ghCLI) Ready(ctx context.Context, n int) error {
	_, err := g.gh(ctx, "pr", "ready", strconv.Itoa(n))
	return err
}
