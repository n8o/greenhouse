// Command greenhouse is the factory's scheduler (see docs/DESIGN.md).
// `tick` and `run` are the scheduler; everything else (status, doctor,
// report) reads.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/n8o/greenhouse/internal/config"
	"github.com/n8o/greenhouse/internal/doctor"
	"github.com/n8o/greenhouse/internal/gate"
	"github.com/n8o/greenhouse/internal/launch"
	"github.com/n8o/greenhouse/internal/ledger"
	"github.com/n8o/greenhouse/internal/plan"
	"github.com/n8o/greenhouse/internal/verify"
)

const usage = `usage: greenhouse <command> [flags]

commands:
  init     write a starter greenhouse.toml for a repo (-repo, -plan, -base)
  status   print every bead in the plan with its stage
  doctor   check this host can run greenhouse unattended (read-only)
  tick     run one scheduler pass (-dry-run prints the decisions only)
  run      run ticks every run.interval, holding .greenhouse/run.lock
  report   roll up the ledger: quota per PR, cycle time, parks, retries,
           rejects, and whether scoped memory is used and helps (--json)
  version  print the greenhouse version

Every command but init and version reads -config (default greenhouse.toml).
`

// newSource builds the plan adapter; tests replace it with a plan.Fake.
type newSource func(cfg config.Config) plan.Source

func bdSource(cfg config.Config) plan.Source { return plan.NewBD(cfg.Repo, cfg.PlanPrefix) }

// deps are the adapters the commands use; tests replace them.
type deps struct {
	source newSource
	host   doctor.Env
	// tools builds the scheduler's adapters for cfg.
	tools func(cfg config.Config) adapters
	// git runs the git reads of `greenhouse report`; nil means verify.Exec.
	git verify.Runner
}

func systemTools(cfg config.Config) adapters {
	return adapters{
		plan:   plan.NewBD(cfg.Repo, cfg.PlanPrefix),
		deck:   launch.NewCLI(),
		gh:     &ghCLI{Repo: cfg.Repo},
		probe:  gate.NewCLI(cfg.Repo),
		runner: verify.Exec{},
		now:    time.Now,
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	d := deps{source: bdSource, host: doctor.System(), tools: systemTools, git: verify.Exec{}}
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr, d))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, d deps) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "init":
		git := d.git
		if git == nil {
			git = verify.Exec{}
		}
		return runInit(ctx, args[1:], stdout, stderr, git)
	case "version":
		fmt.Fprintln(stdout, version())
		return 0
	case "status":
		return status(ctx, args[1:], stdout, stderr, d.source)
	case "doctor":
		return runDoctor(ctx, args[1:], stdout, stderr, d.host)
	case "tick", "run":
		return schedule(ctx, args[0], args[1:], stdout, stderr, d.tools)
	case "report":
		return runReport(ctx, args[1:], stdout, stderr, d.git)
	case "-h", "-help", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "greenhouse: unknown command %q\n%s", args[0], usage)
		return 2
	}
}

func status(ctx context.Context, args []string, stdout, stderr io.Writer, src newSource) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("config", config.DefaultPath, "path to greenhouse.toml")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "greenhouse status: unexpected arguments %q\n", fs.Args())
		return 2
	}
	cfg, err := config.Load(*path)
	if err != nil {
		fmt.Fprintf(stderr, "greenhouse status: %v\n", err)
		return 1
	}
	beads, err := src(cfg).List(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "greenhouse status: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "plan %s in %s: %d beads\n", cfg.PlanPrefix, cfg.Repo, len(beads))
	fmt.Fprintf(stdout, "wip implement<=%d open_prs<=%d · quota 5h<%.2f 7d<%.2f\n\n",
		cfg.WIP.Implement, cfg.WIP.OpenPRs, cfg.Quota.FiveHourMax, cfg.Quota.WeeklyShare)
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "BEAD\tSTATUS\tSTAGE\tTITLE")
	for _, b := range beads {
		stage := b.Stage()
		if stage == "" {
			stage = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", b.ID, b.Status, stage, b.Title)
	}
	if err := tw.Flush(); err != nil {
		fmt.Fprintf(stderr, "greenhouse status: %v\n", err)
		return 1
	}
	return 0
}

// runDoctor prints every doctor check and exits 1 if any failed.
func runDoctor(ctx context.Context, args []string, stdout, stderr io.Writer, host doctor.Env) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("config", config.DefaultPath, "path to greenhouse.toml")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "greenhouse doctor: unexpected arguments %q\n", fs.Args())
		return 2
	}
	rs := doctor.Run(ctx, host, *path)
	doctor.Print(stdout, rs)
	if doctor.Failed(rs) {
		return 1
	}
	return 0
}

// schedule is `greenhouse tick` (one pass) and `greenhouse run` (a pass
// every interval until interrupted). Both hold the run lock, except a dry
// run, which writes nothing.
func schedule(ctx context.Context, cmd string, args []string, stdout, stderr io.Writer, tools func(config.Config) adapters) int {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("config", config.DefaultPath, "path to greenhouse.toml")
	dry := fs.Bool("dry-run", false, "print each decision (bead, stage, event, actions, gate verdicts) and carry out none")
	interval := fs.Duration("interval", 0, "pause between ticks (run only; default run.interval)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "greenhouse %s: unexpected arguments %q\n", cmd, fs.Args())
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "greenhouse %s: %v\n", cmd, err)
		return 1
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return fail(err)
	}
	s, err := newScheduler(ctx, cfg, tools(cfg), stdout, *dry)
	if err != nil {
		return fail(err)
	}
	if !*dry {
		if err := excludeState(ctx, s.runner, cfg.Repo); err != nil {
			return fail(err)
		}
		unlock, err := lock(filepath.Join(s.store.Dir, "run.lock"))
		if err != nil {
			return fail(err)
		}
		defer unlock()
	}
	if cmd == "tick" {
		if err := s.Tick(ctx); err != nil {
			return fail(err)
		}
		return 0
	}
	every := cfg.Run.TickInterval()
	if *interval > 0 {
		every = *interval
	}
	for {
		if err := s.Tick(ctx); err != nil && ctx.Err() == nil {
			// A failed tick is reported and retried; the next one converges.
			fmt.Fprintf(stderr, "greenhouse run: %v\n", err)
		}
		select {
		case <-ctx.Done():
			return 0
		case <-time.After(every):
		}
	}
}

// newScheduler wires a scheduler for cfg. Stage sessions are launched under,
// and polled through, run.parent or else the agent-deck session greenhouse
// runs in. PRs merge into base, or else origin's default branch.
func newScheduler(ctx context.Context, cfg config.Config, a adapters, out io.Writer, dry bool) (*scheduler, error) {
	parent := cfg.Run.Parent
	if parent == "" {
		parent = os.Getenv("AGENTDECK_INSTANCE_ID")
	}
	if parent == "" {
		return nil, errors.New("no parent session: set run.parent in greenhouse.toml or run inside agent-deck")
	}
	base, err := baseBranch(ctx, a.runner, cfg)
	if err != nil {
		return nil, err
	}
	l := launch.New(cfg, a.deck)
	l.Parent = parent
	return &scheduler{adapters: a, cfg: cfg, launcher: l, store: ledger.Open(cfg.Repo),
		local: time.Local, out: out, dry: dry, base: "origin/" + base}, nil
}

// baseBranch is cfg.Base, or else the branch origin/HEAD points at: the
// default branch git clone records. A repo without one must name it, since
// guessing "main" would read the wrong plan and rebase onto the wrong ref.
func baseBranch(ctx context.Context, r verify.Runner, cfg config.Config) (string, error) {
	if cfg.Base != "" {
		return cfg.Base, nil
	}
	out, err := r.Run(ctx, cfg.Repo, "git", "symbolic-ref", "--short", "refs/remotes/origin/HEAD")
	ref := strings.TrimSpace(string(out))
	branch, ok := strings.CutPrefix(ref, "origin/")
	if err != nil || !ok || branch == "" {
		return "", fmt.Errorf("cannot tell which branch PRs merge into: origin/HEAD is not set in %s; "+
			"set base in greenhouse.toml, or run `git remote set-head origin --auto`", cfg.Repo)
	}
	return branch, nil
}
