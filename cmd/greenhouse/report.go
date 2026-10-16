package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/n8o/greenhouse/internal/config"
	"github.com/n8o/greenhouse/internal/launch"
	"github.com/n8o/greenhouse/internal/ledger"
	"github.com/n8o/greenhouse/internal/memory"
	"github.com/n8o/greenhouse/internal/report"
	"github.com/n8o/greenhouse/internal/stage"
	"github.com/n8o/greenhouse/internal/verify"
)

// runReport is `greenhouse report`: the ledger rolled up, joined to nugit's
// usage log, as text or --json. It only reads.
func runReport(ctx context.Context, args []string, stdout, stderr io.Writer, git verify.Runner) int {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("config", config.DefaultPath, "path to greenhouse.toml")
	asJSON := fs.Bool("json", false, "print the report as JSON")
	minSample := fs.Int("min-sample", report.DefaultMinSample, "beads each group needs before loaded-context and no-context outcomes are compared")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "greenhouse report: unexpected arguments %q\n", fs.Args())
		return 2
	}
	if *minSample < 1 {
		fmt.Fprintf(stderr, "greenhouse report: -min-sample must be at least 1, got %d\n", *minSample)
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "greenhouse report: %v\n", err)
		return 1
	}
	if git == nil {
		git = verify.Exec{}
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return fail(err)
	}
	recs, err := ledger.Open(cfg.Repo).Records()
	if err != nil {
		return fail(err)
	}
	lines, err := memory.ReadUsage(ctx, git, cfg.Repo)
	if err != nil {
		return fail(err)
	}
	uses := make([]report.Use, 0, len(lines))
	for _, l := range lines {
		uses = append(uses, report.Use{Time: l.Time, Source: l.Source, Branch: l.Branch,
			AgentSession: l.AgentSession, Component: l.Component, Items: l.Items(), Spec: l.Spec})
	}
	// A run whose branch cannot be read has an unknown first commit, so it is
	// left out of Commits.
	commits := map[string]report.Commit{}
	for _, r := range recs {
		if r.Session == "" {
			continue
		}
		at, ok, err := memory.FirstCommit(ctx, git, cfg.Repo, launch.DefaultBranch(r.Bead, stage.Stage(r.Stage)), r.Start)
		if err == nil {
			commits[r.Session] = report.Commit{At: at, None: !ok}
		}
	}

	rep := report.Build(recs, uses, report.Options{
		LLMStages: config.LLMStages,
		Branch:    launch.DefaultBranch,
		Commits:   commits,
		MinSample: *minSample,
	})
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			return fail(err)
		}
		return 0
	}
	if err := report.Text(stdout, rep); err != nil {
		return fail(err)
	}
	return 0
}
