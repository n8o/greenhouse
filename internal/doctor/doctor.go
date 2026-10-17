// Package doctor checks that a host can run greenhouse unattended. Every
// check only reads: a failing check prints the fix for the human to apply,
// and doctor never applies it (in particular it never writes
// ~/.claude/settings.json).
package doctor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/n8o/greenhouse/internal/config"
	"github.com/n8o/greenhouse/internal/gate"
	"github.com/n8o/greenhouse/internal/launch"
	"github.com/n8o/greenhouse/internal/stage"
)

// IngestCommand is the statusLine command that feeds the quota gate
// (ADR-0002).
const IngestCommand = "agent-deck usage ingest claude"

// Result is one check's outcome.
type Result struct {
	Name   string
	OK     bool
	Detail string
	Fix    string // how to make a failing check pass; "" when it passes
	// Warn marks a check that is not OK but does not stop greenhouse from
	// running unattended; Failed ignores it.
	Warn bool
}

// Env is the host doctor inspects. Tests replace it.
type Env struct {
	// ClaudeDir is Claude Code's user config directory (~/.claude, or
	// $CLAUDE_CONFIG_DIR).
	ClaudeDir string
	LookPath  func(file string) (string, error)
	// Run executes name with args and returns its stdout and stderr apart:
	// agent-deck warns on stderr around the JSON on its stdout.
	Run      func(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error)
	ReadFile func(path string) ([]byte, error)
	// Now is the clock for reading freshness; nil means time.Now.
	Now func() time.Time
}

func (e Env) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// System returns the Env of this host.
func System() Env {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".claude")
	}
	return Env{
		ClaudeDir: dir,
		LookPath:  exec.LookPath,
		Run: func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
			var stdout, stderr bytes.Buffer
			cmd := exec.CommandContext(ctx, name, args...)
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			return stdout.Bytes(), stderr.Bytes(), err
		},
		ReadFile: os.ReadFile,
	}
}

// Tools are the CLIs greenhouse shells out to (AGENTS.md rule 2), in the
// order doctor reports them.
var Tools = []string{"bd", "nugit", "gh", "agent-deck", "jq"}

// Run runs every check against the config at configPath.
func Run(ctx context.Context, env Env, configPath string) []Result {
	var rs []Result
	settingsPath := filepath.Join(env.ClaudeDir, "settings.json")
	settings, err := env.ReadFile(settingsPath)
	sl := StatusLine(settingsPath, settings, err)
	if usage, _, _ := env.Run(ctx, "agent-deck", "usage", "--json"); !sl.OK && FreshUsage(usage, env.now()) {
		sl = Result{Name: sl.Name, OK: true, Detail: "agent-deck usage has a fresh Claude reading (" + sl.Detail + ")"}
	}
	rs = append(rs, sl)
	out, _, err := env.Run(ctx, "agent-deck", "inbox", "writer-status", "--json")
	rs = append(rs, NotifyDaemon(out, err))
	for _, tool := range Tools {
		rs = append(rs, toolCheck(ctx, env, tool))
	}

	cfg, err := config.Read(configPath)
	if err != nil {
		return append(rs, Result{Name: "config", Detail: err.Error(),
			Fix: "fix " + configPath + " (see docs/DESIGN.md); the MCP and model checks need it"})
	}
	if err := cfg.Validate(); err != nil {
		rs = append(rs, Result{Name: "config", Detail: strings.ReplaceAll(err.Error(), "\n", "; "), Fix: "fix " + configPath})
	} else {
		rs = append(rs, Result{Name: "config", OK: true, Detail: configPath + " is valid"})
	}
	base, ref := baseRef(ctx, env, cfg)
	rs = append(rs, base)
	rs = append(rs, mcpCheck(ctx, env, cfg.Repo, ref))
	rs = append(rs, Prompts(cfg), ExportScript(env, cfg.Repo))
	return append(rs, Models(cfg))
}

// Base checks that the branch PRs merge into exists on origin: base, or
// else origin's default branch. The scheduler reads what is done from it,
// branches every stage session from it and rebases every PR onto it.
func Base(ctx context.Context, env Env, cfg config.Config) Result {
	r, _ := baseRef(ctx, env, cfg)
	return r
}

// baseRef is Base plus the ref it found ("" when the check fails).
func baseRef(ctx context.Context, env Env, cfg config.Config) (Result, string) {
	r := Result{Name: "base"}
	if cfg.Base != "" {
		ref := "origin/" + cfg.Base
		if _, _, err := env.Run(ctx, "git", "-C", cfg.Repo, "rev-parse", "--verify", "--quiet", "refs/remotes/"+ref); err != nil {
			r.Detail = ref + " does not exist in " + cfg.Repo
			r.Fix = "set base in the config to the branch PRs merge into, or run `git fetch origin`"
			return r, ""
		}
		r.OK, r.Detail = true, "PRs merge into "+ref
		return r, ref
	}
	out, _, err := env.Run(ctx, "git", "-C", cfg.Repo, "symbolic-ref", "--short", "refs/remotes/origin/HEAD")
	ref := strings.TrimSpace(string(out))
	if err != nil || !strings.HasPrefix(ref, "origin/") || ref == "origin/" {
		r.Detail = "base is unset and origin/HEAD is not set in " + cfg.Repo
		r.Fix = "set base in the config, or run `git -C " + cfg.Repo + " remote set-head origin --auto`"
		return r, ""
	}
	r.OK, r.Detail = true, "PRs merge into "+ref+" (origin's default branch)"
	return r, ref
}

// mcpCheck runs the MCP preflight on ref, the base every stage session
// branches from: the checkout's working tree may be on any branch and
// approve a different set of servers. Without a ref it reads the working
// tree.
func mcpCheck(ctx context.Context, env Env, repo, ref string) Result {
	if ref == "" {
		return MCP(repo, launch.CheckMCPApproved(repo))
	}
	err := launch.CheckMCPApprovedIn(repo+" at "+ref, func(path string) ([]byte, error) {
		out, _, err := env.Run(ctx, "git", "-C", repo, "show", ref+":"+path)
		if err != nil {
			// Base passed, so ref exists: the path is not on it.
			return nil, fmt.Errorf("%s:%s: %w", ref, path, os.ErrNotExist)
		}
		return out, nil
	})
	r := MCP(repo+" at "+ref, err)
	if !r.OK {
		r.Fix = "add the servers to enabledMcpjsonServers in .claude/settings.json and merge it to " + ref
	}
	return r
}

// Prompts checks that every stage template renders: the built-in ones, or
// the directory the config's prompts key names.
func Prompts(cfg config.Config) Result {
	r := Result{Name: "prompts"}
	fsys := launch.PromptsFor(cfg)
	var bad []string
	for _, st := range config.LLMStages {
		req := launch.Request{Bead: "x-1", Title: "t", DoneWhen: "d", Stage: st, Reason: "r", PR: 1}
		if _, err := launch.Render(fsys, req, 1); err != nil {
			bad = append(bad, err.Error())
		}
		if _, err := launch.RenderRetry(fsys, req, 1); err != nil && st == stage.Implement {
			bad = append(bad, err.Error())
		}
	}
	where := "the built-in stage prompts"
	if cfg.Prompts != "" {
		where = cfg.Prompts
	}
	if len(bad) > 0 {
		r.Detail = where + ": " + strings.Join(bad, "; ")
		r.Fix = "fix the templates, or remove the prompts key to use the built-in ones"
		return r
	}
	r.OK, r.Detail = true, where+" render"
	return r
}

// ExportScriptPath is where the implement prompt runs the repo's beads
// export to close its bead in the plan.
const ExportScriptPath = "scripts/beads-export.sh"

// ExportScript warns when repo has no beads export script: an implement
// session then has no export to close its bead with, and the PR leaves the
// plan as it was.
func ExportScript(env Env, repo string) Result {
	path := filepath.Join(repo, ExportScriptPath)
	if _, err := env.ReadFile(path); err != nil {
		return Result{Name: "export", Warn: true,
			Detail: "no " + ExportScriptPath + " in " + repo + ": implement sessions cannot close their bead in the plan",
			Fix:    "add a beads export script (greenhouse's own scripts/beads-export.sh is one), or tell sessions how to export in the repo's agent instructions"}
	}
	return Result{Name: "export", OK: true, Detail: ExportScriptPath + " closes beads in the plan"}
}

// Failed reports whether any check failed. Warnings do not count.
func Failed(rs []Result) bool {
	return slices.ContainsFunc(rs, func(r Result) bool { return !r.OK && !r.Warn })
}

// Print writes rs as one line per check, with the fix under each failure.
func Print(w io.Writer, rs []Result) {
	width := 0
	for _, r := range rs {
		width = max(width, len(r.Name))
	}
	for _, r := range rs {
		verdict := "PASS"
		switch {
		case !r.OK && r.Warn:
			verdict = "WARN"
		case !r.OK:
			verdict = "FAIL"
		}
		fmt.Fprintf(w, "%s  %-*s  %s\n", verdict, width, r.Name, r.Detail)
		if !r.OK && r.Fix != "" {
			for l := range strings.Lines(r.Fix) {
				fmt.Fprintf(w, "      %-*s  %s", width, "", l)
			}
			if !strings.HasSuffix(r.Fix, "\n") {
				fmt.Fprintln(w)
			}
		}
	}
}

// StatusLine checks that Claude Code's statusLine pipes its payload through
// `agent-deck usage ingest claude`, so the quota gate has a reading. The fix
// is the exact snippet to put in settings.json, keeping any existing
// statusLine command by wrapping it.
func StatusLine(path string, settings []byte, readErr error) Result {
	r := Result{Name: "statusline"}
	snippet := func(existing string) string {
		cmd := IngestCommand
		if existing != "" {
			cmd += " -- " + existing
		}
		b, _ := json.Marshal(struct {
			Type    string `json:"type"`
			Command string `json:"command"`
		}{"command", cmd})
		return fmt.Sprintf("add to %s (greenhouse never edits it):\n\"statusLine\": %s", path, b)
	}
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		r.Detail = readErr.Error()
		r.Fix = snippet("")
		return r
	}
	var s struct {
		StatusLine *struct {
			Type    string `json:"type"`
			Command string `json:"command"`
		} `json:"statusLine"`
	}
	if len(settings) > 0 {
		if err := json.Unmarshal(settings, &s); err != nil {
			r.Detail = fmt.Sprintf("parse %s: %v", path, err)
			r.Fix = "fix the JSON in " + path + ", then " + snippet("")
			return r
		}
	}
	switch {
	case s.StatusLine == nil || strings.TrimSpace(s.StatusLine.Command) == "":
		r.Detail = "no statusLine command in " + path + ": the quota gate has no usage reading and runs reactive"
		r.Fix = snippet("")
	case s.StatusLine.Type != "command":
		r.Detail = fmt.Sprintf("statusLine type is %q, not \"command\"", s.StatusLine.Type)
		r.Fix = snippet("")
	case !ingests(s.StatusLine.Command):
		r.Detail = fmt.Sprintf("statusLine command %q does not run %s: the quota gate has no usage reading", s.StatusLine.Command, IngestCommand)
		r.Fix = snippet(s.StatusLine.Command)
	default:
		r.OK = true
		r.Detail = "statusLine feeds the usage cache (" + s.StatusLine.Command + ")"
	}
	return r
}

// ingests reports whether command starts by running agent-deck (by name or
// path), optionally with global flags such as `-p default`, and then either
// `usage ingest claude` or `usage statusline-wrap`. Both feed the usage cache.
func ingests(command string) bool {
	f := strings.Fields(command)
	if len(f) == 0 || filepath.Base(f[0]) != "agent-deck" {
		return false
	}
	f = f[1:]
	for len(f) > 0 && strings.HasPrefix(f[0], "-") {
		skip := 1
		if (f[0] == "-p" || f[0] == "--profile") && len(f) > 1 {
			skip = 2
		}
		f = f[skip:]
	}
	if len(f) < 2 || f[0] != "usage" {
		return false
	}
	return f[1] == "statusline-wrap" || (len(f) >= 3 && f[1] == "ingest" && f[2] == "claude")
}

// FreshUsage reports whether `agent-deck usage --json` output carries a
// fresh Claude reading at now. That is the quota gate's real input, so it
// passes the statusline check whatever the statusLine command looks like.
func FreshUsage(out []byte, now time.Time) bool {
	r, err := gate.ParseUsage(out)
	if err != nil || r == nil {
		return false
	}
	ok, _ := r.Fresh(now)
	return ok
}

// NotifyDaemon checks `agent-deck inbox writer-status --json`. The scheduler
// polls session state itself (ADR-0006), so a missing daemon is only a
// warning: it costs the human laptop notifications via `remote drain`, not
// completions.
func NotifyDaemon(out []byte, runErr error) Result {
	r := Result{Name: "notify-daemon", Warn: true,
		Fix: "optional: run `agent-deck notify-daemon` for laptop notifications (greenhouse polls completions without it)"}
	var st struct {
		Running *bool  `json:"running"`
		Detail  string `json:"detail"`
	}
	if err := json.Unmarshal(out, &st); err != nil || st.Running == nil {
		r.Detail = "cannot read agent-deck inbox writer-status --json"
		if runErr != nil {
			r.Detail += ": " + runErr.Error()
		}
		return r
	}
	switch {
	case *st.Running:
		r.OK, r.Warn, r.Fix = true, false, ""
		r.Detail = "a notify-daemon is recording session transitions"
	case st.Detail != "":
		r.Detail = "not running: " + st.Detail
	default:
		r.Detail = "not running"
	}
	return r
}

func toolCheck(ctx context.Context, env Env, tool string) Result {
	r := Result{Name: tool}
	path, err := env.LookPath(tool)
	if err != nil {
		r.Detail = "not on PATH"
		r.Fix = "install " + tool + " and put it on PATH"
		return r
	}
	switch tool {
	case "nugit":
		// scripts/beads-export.sh needs `plan normalize -merge-base`: without
		// it an export from a bd database behind main reverts main (nugit
		// ADR-0047). An older nugit prints `unknown command "plan"`, or a
		// normalize usage with no -merge-base.
		stdout, stderr, _ := env.Run(ctx, "nugit", "plan", "normalize", "-h")
		out := append(stdout, stderr...)
		if bytes.Contains(out, []byte(`unknown command "plan"`)) || !bytes.Contains(out, []byte("-merge-base")) {
			r.Detail = path + " cannot merge an export with main (no `plan normalize -merge-base`)"
			r.Fix = "upgrade nugit to v0.3.1-0.20261017121324-9504b2946efe or later"
			return r
		}
		r.Detail = path + " (has `plan normalize -merge-base`)"
	case "gh":
		if _, _, err := env.Run(ctx, "gh", "auth", "status"); err != nil {
			r.Detail = path + " is not authenticated"
			r.Fix = "run `gh auth login`"
			return r
		}
		r.Detail = path + " (authenticated)"
	default:
		r.Detail = path
	}
	r.OK = true
	return r
}

// MCP reports launch.CheckMCPApproved for repo.
func MCP(repo string, err error) Result {
	r := Result{Name: "mcp"}
	if err != nil {
		r.Detail = err.Error()
		r.Fix = "add the servers to enabledMcpjsonServers in " + filepath.Join(repo, ".claude", "settings.json")
		return r
	}
	r.OK = true
	r.Detail = "every .mcp.json server in " + repo + " is pre-approved (or there are none)"
	return r
}

// Models fails if a configured stage uses a model Claude Code starts in
// manual mode, where an unattended session stalls on its first prompt.
func Models(cfg config.Config) Result {
	r := Result{Name: "models"}
	var bad []string
	for _, name := range slices.Sorted(maps.Keys(cfg.Stages)) {
		if m := cfg.Stages[name].Model; config.ManualMode(m) {
			bad = append(bad, fmt.Sprintf("stages.%s.model %q", name, m))
		}
	}
	if len(bad) > 0 {
		r.Detail = strings.Join(bad, ", ") + " start in manual mode"
		r.Fix = "use sonnet or opus for these stages"
		return r
	}
	r.OK = true
	r.Detail = "no stage uses a manual-mode model"
	return r
}
