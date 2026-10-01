// Command greenhouse is the factory's scheduler (see docs/DESIGN.md).
// Bootstrapping: the subcommands arrive with plan steps gr-boot-1..6.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/n8o/greenhouse/internal/config"
	"github.com/n8o/greenhouse/internal/doctor"
	"github.com/n8o/greenhouse/internal/plan"
)

const usage = `usage: greenhouse <command> [flags]

commands:
  status   print every bead in the plan with its stage
  doctor   check this host can run greenhouse unattended (read-only)
`

// newSource builds the plan adapter; tests replace it with a plan.Fake.
type newSource func(cfg config.Config) plan.Source

func bdSource(cfg config.Config) plan.Source { return plan.NewBD(cfg.Repo, cfg.PlanPrefix) }

// deps are the adapters the commands use; tests replace them.
type deps struct {
	source newSource
	host   doctor.Env
}

func main() {
	d := deps{source: bdSource, host: doctor.System()}
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr, d))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, d deps) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "status":
		return status(ctx, args[1:], stdout, stderr, d.source)
	case "doctor":
		return runDoctor(ctx, args[1:], stdout, stderr, d.host)
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
