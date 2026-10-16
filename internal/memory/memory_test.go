package memory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// reply is a canned answer of fake.
type reply struct {
	Out string
	Err error
}

// fake is a Runner that answers by the command line; a command it has no
// reply for succeeds with no output.
type fake struct{ Replies map[string]reply }

func (f *fake) Run(_ context.Context, _, name string, args ...string) ([]byte, error) {
	r := f.Replies[strings.Join(append([]string{name}, args...), " ")]
	return []byte(r.Out), r.Err
}

func TestCommonRoot(t *testing.T) {
	ctx := context.Background()
	for _, tt := range []struct {
		name string
		rep  reply
		want string
	}{
		{"main checkout", reply{Out: ".git\n"}, "/repo"},
		{"linked worktree", reply{Out: "/main/.git\n"}, "/main"},
		{"not git", reply{Err: errors.New("not a git repository")}, "/repo"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := &fake{Replies: map[string]reply{"git rev-parse --git-common-dir": tt.rep}}
			if got := CommonRoot(ctx, f, "/repo"); got != tt.want {
				t.Errorf("CommonRoot = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReadUsage(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".nugit", ".cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join("testdata", "usage.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, LogPath), data, 0o644); err != nil {
		t.Fatal(err)
	}
	// The worktree's common dir points at root's .git.
	f := &fake{Replies: map[string]reply{
		"git rev-parse --git-common-dir": {Out: filepath.Join(root, ".git")},
	}}
	lines, err := ReadUsage(context.Background(), f, filepath.Join(root, "wt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 3 {
		t.Fatalf("read %d lines, want 3 (malformed and blank skipped): %+v", len(lines), lines)
	}
	cli, hook, mcp := lines[0], lines[1], lines[2]
	if cli.Source != "cli" || cli.Branch != "feature/gr-boot-1" || cli.Component != "cli" || cli.Items() != 1 || cli.Spec {
		t.Errorf("cli line = %+v", cli)
	}
	if want := time.Date(2026, 9, 29, 20, 40, 30, 0, time.UTC); !cli.Time.Truncate(time.Second).Equal(want) {
		t.Errorf("time = %v, want %v", cli.Time, want)
	}
	if hook.Source != "hook" || hook.AgentSession != "abc-123" {
		t.Errorf("hook line = %+v", hook)
	}
	if mcp.Source != "mcp" || mcp.Component != "" || mcp.Items() != 6 || !mcp.Spec {
		t.Errorf("mcp line = %+v", mcp)
	}
}

func TestReadUsageMissing(t *testing.T) {
	lines, err := ReadUsage(context.Background(), &fake{}, t.TempDir())
	if err != nil || lines != nil {
		t.Errorf("missing log = %v, %v; want no data and no error", lines, err)
	}
}

func TestFirstCommit(t *testing.T) {
	ctx := context.Background()
	since := time.Date(2026, 10, 1, 12, 0, 0, 0, time.FixedZone("x", 3600))
	local := "git log --reverse --format=%cI --since=2026-10-01T11:00:00Z feature/b-implement --"
	remote := "git log --reverse --format=%cI --since=2026-10-01T11:00:00Z origin/feature/b-implement --"
	gone := reply{Err: errors.New("unknown revision")}
	for _, tt := range []struct {
		name    string
		replies map[string]reply
		want    time.Time
		ok      bool
		err     bool
	}{
		{"first of several", map[string]reply{local: {Out: "2026-10-01T11:05:00Z\n2026-10-01T11:30:00Z\n"}},
			time.Date(2026, 10, 1, 11, 5, 0, 0, time.UTC), true, false},
		{"no commit", map[string]reply{local: {Out: "\n"}}, time.Time{}, false, false},
		{"remote only", map[string]reply{local: gone, remote: {Out: "2026-10-01T12:00:00+01:00\n"}},
			time.Date(2026, 10, 1, 11, 0, 0, 0, time.UTC), true, false},
		{"branch gone", map[string]reply{local: gone, remote: gone}, time.Time{}, false, true},
		{"bad date", map[string]reply{local: {Out: "yesterday\n"}}, time.Time{}, false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, ok, err := FirstCommit(ctx, &fake{Replies: tt.replies}, "/repo", "feature/b-implement", since)
			if (err != nil) != tt.err || ok != tt.ok || !got.Equal(tt.want) {
				t.Errorf("FirstCommit = %v, %v, %v; want %v, %v, err %v", got, ok, err, tt.want, tt.ok, tt.err)
			}
		})
	}
}
