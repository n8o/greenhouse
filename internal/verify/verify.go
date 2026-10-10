// Package verify is the deterministic verify stage (ADR-0004): it runs the
// configured commands in a PR worktree, then checks the PR against the plan
// scope rule (ADR-0005). No LLM is involved; the verdict maps onto a
// stage.Event and internal/stage decides what happens next.
package verify

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/n8o/greenhouse/internal/stage"
)

// DefaultTimeout bounds each verify command when greenhouse.toml sets none.
const DefaultTimeout = 10 * time.Minute

// MaxLogBytes caps the log that goes back to implement: enough for the head
// and tail of a failing command's output, small enough to fit a prompt.
const MaxLogBytes = 8 << 10

// PlanScopeName is the name of the plan-scope check in a Result.
const PlanScopeName = "plan-scope"

// Runner executes a program in dir and returns its combined stdout and
// stderr. Run reports a non-zero exit or a cancelled ctx as an error, with
// whatever output there was. Tests use a Fake.
type Runner interface {
	Run(ctx context.Context, dir, name string, args ...string) ([]byte, error)
}

// Options says what to verify.
type Options struct {
	Dir      string   // the PR worktree
	Base     string   // ref the PR merges into, e.g. "origin/main"
	Bead     string   // the bead this PR closes (ADR-0005)
	Commands []string // [verify] commands, run through `sh -c`, in order
	Timeout  time.Duration
	MaxLog   int // 0 means MaxLogBytes
}

// Check is one check's outcome.
type Check struct {
	Name   string // the command, or PlanScopeName
	OK     bool
	Detail string
}

// Result is the verdict. Checks ends at the first failure, so a failed
// Result's last check is the failing one.
type Result struct {
	Checks []Check
	// Log is empty on a pass. On a failure it is the bounded log that goes
	// back to implement: the failing check, why, and the head and tail of
	// its output.
	Log string
}

// OK reports whether every check passed.
func (r Result) OK() bool { return r.Log == "" }

// Event maps r onto the state machine's input: VerifyPass with the PR
// number, or VerifyFail with the log and how many retries are already used.
func (r Result) Event(pr, retries int) stage.Event {
	if r.OK() {
		return stage.Event{Kind: stage.VerifyPass, PR: pr}
	}
	return stage.Event{Kind: stage.VerifyFail, Reason: r.Log, Retries: retries}
}

// Print writes r as one line per check, in the format of `greenhouse doctor`.
func Print(w io.Writer, r Result) {
	width := 0
	for _, c := range r.Checks {
		width = max(width, len(c.Name))
	}
	for _, c := range r.Checks {
		verdict := "PASS"
		if !c.OK {
			verdict = "FAIL"
		}
		fmt.Fprintf(w, "%s  %-*s  %s\n", verdict, width, c.Name, c.Detail)
	}
	if r.Log != "" {
		fmt.Fprintf(w, "\n%s\n", r.Log)
	}
}

// Run checks the PR's plan scope, then runs o.Commands in order, stopping at
// the first failure. An error is returned only when a check could not run at
// all (a failing check is a failed Result, not an error).
func Run(ctx context.Context, r Runner, o Options) (Result, error) {
	maxLog := o.MaxLog
	if maxLog <= 0 {
		maxLog = MaxLogBytes
	}
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	var res Result

	bad, err := CheckPlanScope(ctx, r, o.Dir, o.Base, o.Bead)
	if err != nil {
		return Result{}, err
	}
	if len(bad) > 0 {
		detail := strings.Join(bad, "; ")
		res.Checks = append(res.Checks, Check{Name: PlanScopeName, Detail: detail})
		res.Log = bound(fmt.Sprintf("%s failed: %s\nA PR may change only the line of its own bead (%s) under .beads/plans/, and only to close it (ADR-0005). Restore the other lines with `git checkout %s -- .beads/plans/` and re-add only your bead's line.\n",
			PlanScopeName, detail, o.Bead, o.Base), "", maxLog)
		return res, nil
	}
	res.Checks = append(res.Checks, Check{Name: PlanScopeName, OK: true, Detail: "only " + o.Bead + " changes, and only to close it"})

	for _, cmd := range o.Commands {
		cctx, cancel := context.WithTimeout(ctx, timeout)
		out, err := r.Run(cctx, o.Dir, "sh", "-c", cmd)
		timedOut := errors.Is(cctx.Err(), context.DeadlineExceeded)
		cancel()
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		if err == nil && !timedOut {
			res.Checks = append(res.Checks, Check{Name: cmd, OK: true})
			continue
		}
		why := err.Error()
		if timedOut {
			why = fmt.Sprintf("timed out after %s", timeout)
		}
		res.Checks = append(res.Checks, Check{Name: cmd, Detail: why})
		res.Log = bound("$ "+cmd+"\n"+why+"\n", string(out), maxLog)
		return res, nil
	}
	return res, nil
}

// bound returns header followed by the head and tail of output, at most max
// bytes in all, with a marker where the middle was dropped.
func bound(header, output string, max int) string {
	if len(header) >= max {
		return strings.ToValidUTF8(header[:max], "")
	}
	room := max - len(header)
	if len(output) <= room {
		return header + output
	}
	marker := func(n int) string { return fmt.Sprintf("\n… %d bytes elided …\n", n) }
	room -= len(marker(len(output))) // the real count has no more digits
	if room <= 0 {
		return header
	}
	head, tail := output[:room/2], output[len(output)-(room-room/2):]
	return header + strings.ToValidUTF8(head, "") + marker(len(output)-room) + strings.ToValidUTF8(tail, "")
}

// Exec is the Runner that runs real programs.
type Exec struct{}

// Run implements Runner. The program gets its own process group, killed as a
// whole on timeout so a `sh -c` child cannot outlive its command.
func (Exec) Run(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	return cmd.CombinedOutput()
}
