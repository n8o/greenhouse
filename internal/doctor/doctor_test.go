package doctor

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/n8o/greenhouse/internal/config"
)

const settingsPath = "/home/u/.claude/settings.json"

func TestStatusLine(t *testing.T) {
	tests := []struct {
		name     string
		settings string
		readErr  error
		ok       bool
		detail   string
		fix      string
	}{
		{"wired", `{"statusLine":{"type":"command","command":"agent-deck usage ingest claude"}}`, nil, true, "statusLine runs agent-deck usage ingest claude", ""},
		{"wired by path, wrapping", `{"statusLine":{"type":"command","command":"/opt/bin/agent-deck usage ingest claude -- ~/bin/line.sh"}}`, nil, true, "statusLine runs", ""},
		{"no statusLine", `{"model":"opus"}`, nil, false, "no statusLine command",
			`"statusLine": {"type":"command","command":"agent-deck usage ingest claude"}`},
		{"no settings file", "", fs.ErrNotExist, false, "no statusLine command",
			`"statusLine": {"type":"command","command":"agent-deck usage ingest claude"}`},
		{"other command is wrapped", `{"statusLine":{"type":"command","command":"~/bin/line.sh --short"}}`, nil, false, `"~/bin/line.sh --short" does not run`,
			`"statusLine": {"type":"command","command":"agent-deck usage ingest claude -- ~/bin/line.sh --short"}`},
		{"ingest later in a pipeline does not count", `{"statusLine":{"type":"command","command":"cat | agent-deck usage ingest claude"}}`, nil, false, "does not run",
			`agent-deck usage ingest claude -- cat | agent-deck usage ingest claude`},
		{"wrong ingest provider", `{"statusLine":{"type":"command","command":"agent-deck usage ingest codex"}}`, nil, false, "does not run", ""},
		{"not a command statusLine", `{"statusLine":{"type":"static","command":"agent-deck usage ingest claude"}}`, nil, false, `type is "static"`, ""},
		{"bad JSON", `{`, nil, false, "parse " + settingsPath, "fix the JSON"},
		{"unreadable", "", errors.New("permission denied"), false, "permission denied", "add to " + settingsPath},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := StatusLine(settingsPath, []byte(tt.settings), tt.readErr)
			if r.OK != tt.ok || !strings.Contains(r.Detail, tt.detail) || !strings.Contains(r.Fix, tt.fix) {
				t.Errorf("got %+v\nwant ok=%v detail~%q fix~%q", r, tt.ok, tt.detail, tt.fix)
			}
			if tt.ok != (r.Fix == "") {
				t.Errorf("fix %q with ok=%v", r.Fix, r.OK)
			}
			if !tt.ok && !strings.Contains(r.Fix, "never edits") {
				t.Errorf("fix %q should say doctor does not edit settings", r.Fix)
			}
		})
	}
}

func TestNotifyDaemon(t *testing.T) {
	tests := []struct {
		name   string
		out    string
		err    error
		ok     bool
		detail string
	}{
		{"running", `{"running":true,"last_heartbeat":"2026-09-30T20:00:00Z","detail":""}`, nil, true, "is recording"},
		{"not running", `{"running":false,"detail":"no notify-daemon has ever stamped a heartbeat on this host"}`, nil, false, "not running: no notify-daemon has ever"},
		{"not running, no detail", `{"running":false}`, nil, false, "not running"},
		{"agent-deck missing", "", errors.New("exec: not found"), false, "cannot read agent-deck inbox writer-status --json: exec: not found"},
		{"old agent-deck", "unknown command", errors.New("exit status 1"), false, "cannot read"},
		{"no running field", `{}`, nil, false, "cannot read"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NotifyDaemon([]byte(tt.out), tt.err)
			if r.OK != tt.ok || !strings.Contains(r.Detail, tt.detail) {
				t.Errorf("got %+v, want ok=%v detail~%q", r, tt.ok, tt.detail)
			}
			if !r.OK && !strings.Contains(r.Fix, "agent-deck notify-daemon") {
				t.Errorf("fix %q", r.Fix)
			}
		})
	}
}

func TestModels(t *testing.T) {
	ok := config.Config{Stages: map[string]config.Stage{"spec": {Model: "sonnet"}, "implement": {Model: "opus"}}}
	if r := Models(ok); !r.OK {
		t.Errorf("got %+v", r)
	}
	bad := config.Config{Stages: map[string]config.Stage{"spec": {Model: "Haiku"}, "learn": {Model: "claude-haiku-4-5"}, "implement": {Model: "opus"}}}
	r := Models(bad)
	if r.OK || r.Detail != `stages.learn.model "claude-haiku-4-5", stages.spec.model "Haiku" start in manual mode` {
		t.Errorf("got %+v", r)
	}
}

func TestMCP(t *testing.T) {
	if r := MCP("/r", nil); !r.OK {
		t.Errorf("got %+v", r)
	}
	if r := MCP("/r", errors.New("not pre-approved: x")); r.OK || r.Fix != "add the servers to enabledMcpjsonServers in /r/.claude/settings.json" {
		t.Errorf("got %+v", r)
	}
}

// fakeEnv is a host where every tool is installed and healthy unless a test
// changes it.
type fakeEnv struct {
	missing  map[string]bool
	outs     map[string]string // "name args" -> stdout
	errOuts  map[string]string // "name args" -> stderr
	errs     map[string]error
	settings string
}

func (f *fakeEnv) env() Env {
	return Env{
		ClaudeDir: "/home/u/.claude",
		LookPath: func(file string) (string, error) {
			if f.missing[file] {
				return "", errors.New("not found")
			}
			return "/bin/" + file, nil
		},
		Run: func(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
			key := name + " " + strings.Join(args, " ")
			return []byte(f.outs[key]), []byte(f.errOuts[key]), f.errs[key]
		},
		ReadFile: func(path string) ([]byte, error) {
			if path != settingsPath {
				return nil, fs.ErrNotExist
			}
			return []byte(f.settings), nil
		},
	}
}

func healthy() *fakeEnv {
	return &fakeEnv{
		missing:  map[string]bool{},
		outs:     map[string]string{"agent-deck inbox writer-status --json": `{"running":true}`},
		errOuts:  map[string]string{"nugit plan": "usage: nugit plan <check|normalize> [flags]\n"},
		errs:     map[string]error{},
		settings: `{"statusLine":{"type":"command","command":"agent-deck usage ingest claude"}}`,
	}
}

func writeConfig(t *testing.T, implementModel string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "greenhouse.toml")
	body := `plan_prefix = "gr-boot"
[verify]
commands = ["go test ./..."]
[stages.spec]
model = "sonnet"
effort = "medium"
max_turns = 5
[stages.implement]
model = "` + implementModel + `"
effort = "high"
max_turns = 5
[stages.learn]
model = "sonnet"
effort = "low"
max_turns = 5
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func names(rs []Result, ok bool) []string {
	var out []string
	for _, r := range rs {
		if r.OK == ok {
			out = append(out, r.Name)
		}
	}
	return out
}

func TestRun(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name   string
		edit   func(*fakeEnv)
		model  string
		failed []string
	}{
		{"healthy", func(*fakeEnv) {}, "opus", nil},
		{"statusLine not wired", func(f *fakeEnv) { f.settings = `{}` }, "opus", []string{"statusline"}},
		{"daemon down", func(f *fakeEnv) { f.outs["agent-deck inbox writer-status --json"] = `{"running":false}` }, "opus", []string{"notify-daemon"}},
		{"jq and bd missing", func(f *fakeEnv) { f.missing["jq"], f.missing["bd"] = true, true }, "opus", []string{"bd", "jq"}},
		{"nugit without plan", func(f *fakeEnv) { f.errOuts["nugit plan"] = `unknown command "plan"` }, "opus", []string{"nugit"}},
		{"gh logged out", func(f *fakeEnv) { f.errs["gh auth status"] = errors.New("exit status 1") }, "opus", []string{"gh"}},
		{"haiku stage", func(*fakeEnv) {}, "haiku", []string{"config", "models"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := healthy()
			tt.edit(f)
			rs := Run(ctx, f.env(), writeConfig(t, tt.model))
			want := []string{"statusline", "notify-daemon", "bd", "nugit", "gh", "agent-deck", "jq", "config", "mcp", "models"}
			if got := names(rs, true); len(got)+len(names(rs, false)) != len(want) {
				t.Fatalf("ran %d checks, want %d: %+v", len(rs), len(want), rs)
			}
			if got := names(rs, false); strings.Join(got, ",") != strings.Join(tt.failed, ",") {
				t.Errorf("failed %v, want %v: %+v", got, tt.failed, rs)
			}
			if Failed(rs) != (len(tt.failed) > 0) {
				t.Errorf("Failed = %v", Failed(rs))
			}
		})
	}
}

func TestRunMissingConfig(t *testing.T) {
	rs := Run(context.Background(), healthy().env(), filepath.Join(t.TempDir(), "nope.toml"))
	last := rs[len(rs)-1]
	if last.Name != "config" || last.OK || !Failed(rs) {
		t.Errorf("got %+v", rs)
	}
}

func TestPrint(t *testing.T) {
	var b bytes.Buffer
	Print(&b, []Result{
		{Name: "jq", OK: true, Detail: "/bin/jq"},
		{Name: "statusline", Detail: "no statusLine", Fix: "add to x:\n\"statusLine\": {}"},
	})
	want := "PASS  jq          /bin/jq\n" +
		"FAIL  statusline  no statusLine\n" +
		"                  add to x:\n" +
		"                  \"statusLine\": {}\n"
	if b.String() != want {
		t.Errorf("got\n%s\nwant\n%s", b.String(), want)
	}
}
