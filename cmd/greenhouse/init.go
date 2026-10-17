package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"text/template"

	"github.com/n8o/greenhouse/internal/config"
	"github.com/n8o/greenhouse/internal/verify"
)

// starter is the greenhouse.toml `greenhouse init` writes. It validates as
// written; the verify commands are the part a repo must fill in.
var starter = template.Must(template.New("greenhouse.toml").Parse(`# greenhouse.toml — how greenhouse runs a plan on a repo.
# Reference: https://github.com/n8o/greenhouse/blob/main/docs/DESIGN.md

# Repository to work on; a relative path is resolved against this file.
repo = {{printf "%q" .Repo}}
# Beads whose ID is "<plan_prefix>-<n>" make up the plan.
plan_prefix = {{printf "%q" .Plan}}
# Branch PRs merge into, on origin; unset means origin's default branch.
base = {{printf "%q" .Base}}
# A directory of stage templates that replaces the built-in ones; unset
# uses the built-in ones.
# prompts = "greenhouse-prompts"

[wip]
implement = 1 # concurrent implement sessions
open_prs = 3  # open agent PRs; no new implement launch at or above this

[quota] # fractions of each Claude usage window
five_hour_max = 0.8 # launch only while 5-hour usage is below this
weekly_share = 0.5  # launch only while 7-day usage is below this

[verify] # deterministic checks on a PR branch, run in order
timeout = "10m" # per command; a hung command fails verify
commands = [
  # Add this repo's build, test and lint commands here: a PR is marked
  # ready only when every one of them passes.
  "nugit pr-render -base {{.Base}} -head HEAD -fail-on fail",
]

# Model, reasoning effort and turn budget of each LLM stage. Model and
# effort are always pinned: an unpinned session inherits the user's Claude
# Code default. timeout is the session's hard wall-clock limit.
[stages.spec]
model = "sonnet"
effort = "medium"
max_turns = 20
timeout = "30m"

[stages.implement]
model = "opus"
effort = "high"
max_turns = 80
timeout = "2h"

[stages.learn]
model = "sonnet"
effort = "low"
max_turns = 15
timeout = "30m"

[run] # the scheduler loop (greenhouse tick / run)
interval = "5m" # pause between ticks of ` + "`greenhouse run`" + `
`))

// runInit is `greenhouse init`: it writes a starter config for a repo, so a
// greenhouse installed with `go install` needs no checkout of its own.
func runInit(ctx context.Context, args []string, stdout, stderr io.Writer, r verify.Runner) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", ".", "repository greenhouse works on")
	prefix := fs.String("plan", "", "plan prefix: the plan's beads are <plan>-<n> (required)")
	base := fs.String("base", "", "branch PRs merge into (default: origin's default branch)")
	out := fs.String("config", config.DefaultPath, "path of the config to write")
	force := fs.Bool("force", false, "overwrite an existing config")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "greenhouse init: unexpected arguments %q\n", fs.Args())
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "greenhouse init: %v\n", err)
		return 1
	}
	if *prefix == "" {
		fmt.Fprintln(stderr, "greenhouse init: -plan is required")
		return 2
	}
	abs, err := filepath.Abs(*repo)
	if err != nil {
		return fail(err)
	}
	branch, err := baseBranch(ctx, r, config.Config{Repo: abs, Base: *base})
	if err != nil {
		return fail(fmt.Errorf("%w (or pass -base)", err))
	}
	if _, err := os.Stat(*out); err == nil && !*force {
		return fail(fmt.Errorf("%s exists; pass -force to overwrite it", *out))
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fail(err)
	}
	var b strings.Builder
	if err := starter.Execute(&b, struct{ Repo, Plan, Base string }{relRepo(*out, abs), *prefix, branch}); err != nil {
		return fail(err)
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		return fail(err)
	}
	if err := os.WriteFile(*out, []byte(b.String()), 0o644); err != nil {
		return fail(err)
	}
	fmt.Fprintf(stdout, "wrote %s for plan %s on %s (base %s)\n", *out, *prefix, abs, branch)
	fmt.Fprintln(stdout, "next: add the repo's build and test commands to verify.commands, then run `greenhouse doctor`")
	return 0
}

// relRepo is repo as the config at path should name it: relative when the
// config sits in or above the repo, absolute otherwise.
func relRepo(path, repo string) string {
	dir, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return repo
	}
	rel, err := filepath.Rel(dir, repo)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return repo
	}
	return rel
}

// version is the module version `go install` stamped, plus the commit a
// local build was made from.
func version() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "greenhouse (unknown version)"
	}
	v := info.Main.Version
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" && len(s.Value) >= 12 {
			v += " " + s.Value[:12]
		}
	}
	return "greenhouse " + v
}
