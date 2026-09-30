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

	"github.com/n8o/greenhouse/internal/config"
	"github.com/n8o/greenhouse/internal/launch"
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
	rs = append(rs, StatusLine(settingsPath, settings, err))
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
	rs = append(rs, MCP(cfg.Repo, launch.CheckMCPApproved(cfg.Repo)))
	return append(rs, Models(cfg))
}

// Failed reports whether any check failed.
func Failed(rs []Result) bool {
	return slices.ContainsFunc(rs, func(r Result) bool { return !r.OK })
}

// Print writes rs as one line per check, with the fix under each failure.
func Print(w io.Writer, rs []Result) {
	width := 0
	for _, r := range rs {
		width = max(width, len(r.Name))
	}
	for _, r := range rs {
		verdict := "PASS"
		if !r.OK {
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
		r.Detail = "statusLine runs " + IngestCommand
	}
	return r
}

// ingests reports whether command starts by running agent-deck (by name or
// path) with `usage ingest claude`.
func ingests(command string) bool {
	f := strings.Fields(command)
	return len(f) >= 4 && filepath.Base(f[0]) == "agent-deck" &&
		f[1] == "usage" && f[2] == "ingest" && f[3] == "claude"
}

// NotifyDaemon checks `agent-deck inbox writer-status --json`: without a
// running notify-daemon nothing records session transitions, so completion
// events never reach greenhouse (ADR-0003).
func NotifyDaemon(out []byte, runErr error) Result {
	r := Result{Name: "notify-daemon",
		Fix: "run `agent-deck notify-daemon` and keep it running (a login item or service on an always-on host)"}
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
		r.OK, r.Fix = true, ""
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
		// nugit prints `unknown command "plan"` when it predates plan.
		stdout, stderr, _ := env.Run(ctx, "nugit", "plan")
		out := append(stdout, stderr...)
		if bytes.Contains(out, []byte(`unknown command "plan"`)) || !bytes.Contains(out, []byte("nugit plan")) {
			r.Detail = path + " has no `plan` subcommand"
			r.Fix = "upgrade nugit to a version with `nugit plan check|normalize`"
			return r
		}
		r.Detail = path + " (has `plan`)"
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
