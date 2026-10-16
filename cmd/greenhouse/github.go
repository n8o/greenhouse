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
	"time"

	"github.com/n8o/greenhouse/internal/gate"
)

// GitHub is the gh CLI as the scheduler uses it. Every write is safe to
// repeat, so a tick that crashed halfway can redo it.
type GitHub interface {
	// EnsureLabel creates the gate.PRLabel label if the repo lacks it.
	EnsureLabel(ctx context.Context) error
	// PR returns pull request n.
	PR(ctx context.Context, n int) (PR, error)
	// PRByBranch returns the most recent pull request, in any state, whose
	// head is branch, with its Files; Number 0 if there is none.
	PRByBranch(ctx context.Context, branch string) (PR, error)
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
	// Reason is the body of the latest non-empty review or PR comment, the
	// human's why for a closed PR (`gh pr close -c` leaves a comment); ""
	// if there is none.
	Reason string
	// Files are the paths the PR changes; filled by PRByBranch only.
	Files []string
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
	out, err := g.gh(ctx, "pr", "view", strconv.Itoa(n), "--json", "number,state,isDraft,headRefName,labels,reviews,comments")
	if err != nil {
		return PR{}, err
	}
	return parsePR(out)
}

func (g *ghCLI) PRByBranch(ctx context.Context, branch string) (PR, error) {
	out, err := g.gh(ctx, "pr", "list", "--head", branch, "--state", "all",
		"--json", "number,state,isDraft,headRefName,files", "--limit", "10")
	if err != nil {
		return PR{}, err
	}
	return parsePRList(out)
}

// ghPR is the part of gh's pull request JSON the scheduler reads.
type ghPR struct {
	Number      int       `json:"number"`
	State       string    `json:"state"`
	IsDraft     bool      `json:"isDraft"`
	HeadRefName string    `json:"headRefName"`
	Labels      []ghLabel `json:"labels"`
	Reviews     []struct {
		Body        string    `json:"body"`
		SubmittedAt time.Time `json:"submittedAt"`
	} `json:"reviews"`
	Comments []struct {
		Body      string    `json:"body"`
		CreatedAt time.Time `json:"createdAt"`
	} `json:"comments"`
	Files []struct {
		Path string `json:"path"`
	} `json:"files"`
}

func (v ghPR) pr() PR {
	pr := PR{Number: v.Number, State: v.State, IsDraft: v.IsDraft, HeadRef: v.HeadRefName}
	for _, l := range v.Labels {
		pr.Labels = append(pr.Labels, l.Name)
	}
	for _, f := range v.Files {
		pr.Files = append(pr.Files, f.Path)
	}
	// The latest word wins, whether a review or a comment; ties keep the
	// review, which gh lists first.
	var at time.Time
	note := func(body string, when time.Time) {
		if b := strings.TrimSpace(body); b != "" && (pr.Reason == "" || when.After(at)) {
			pr.Reason, at = b, when
		}
	}
	for _, r := range v.Reviews {
		note(r.Body, r.SubmittedAt)
	}
	for _, c := range v.Comments {
		note(c.Body, c.CreatedAt)
	}
	return pr
}

// parsePR parses `gh pr view --json`.
func parsePR(out []byte) (PR, error) {
	var v ghPR
	if err := json.Unmarshal(out, &v); err != nil {
		return PR{}, fmt.Errorf("parse gh pr view --json: %w", err)
	}
	return v.pr(), nil
}

// parsePRList parses `gh pr list --json` and returns its newest PR, the
// highest number; the zero PR if the list is empty.
func parsePRList(out []byte) (PR, error) {
	if len(bytes.TrimSpace(out)) == 0 {
		return PR{}, nil // gh prints nothing, not [], when nothing matches
	}
	var vs []ghPR
	if err := json.Unmarshal(out, &vs); err != nil {
		return PR{}, fmt.Errorf("parse gh pr list --json: %w", err)
	}
	var newest PR
	for _, v := range vs {
		if v.Number > newest.Number {
			newest = v.pr()
		}
	}
	return newest, nil
}

func (g *ghCLI) AddLabel(ctx context.Context, n int) error {
	_, err := g.gh(ctx, "pr", "edit", strconv.Itoa(n), "--add-label", gate.PRLabel)
	return err
}

func (g *ghCLI) Ready(ctx context.Context, n int) error {
	_, err := g.gh(ctx, "pr", "ready", strconv.Itoa(n))
	return err
}
