// Package memory reads what greenhouse needs to judge scoped memory: nugit's
// local usage log of context calls, and when a stage run first committed. It
// reads the log as a plain file and shells out to git; it does not import
// nugit.
package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// LogPath is nugit's usage log, relative to the repo's common root (the main
// checkout), so every worktree's calls land in one log.
const LogPath = ".nugit/.cache/usage.jsonl"

// Runner executes a program in dir and returns its output; verify.Exec runs
// real programs and verify.Fake answers in tests.
type Runner interface {
	Run(ctx context.Context, dir, name string, args ...string) ([]byte, error)
}

// Line is one usage log line: a served context call (source cli or mcp) or
// an edit the pre-edit hook saw (source hook). Unknown fields are ignored.
type Line struct {
	Time          time.Time `json:"time"`
	Source        string    `json:"source"`
	Branch        string    `json:"branch"`
	AgentSession  string    `json:"agent_session"` // agent-deck instance id
	Path          string    `json:"path"`
	Component     string    `json:"component"` // "" = no component owns the path
	Decisions     int       `json:"decisions"`
	Lessons       int       `json:"lessons"`
	References    int       `json:"references"`
	Glossary      int       `json:"glossary"`
	WorkingMemory int       `json:"working_memory"`
	PathHistory   int       `json:"path_history"`
	Spec          bool      `json:"spec"`
}

// Items is how many knowledge items the call's bundle carried.
func (l Line) Items() int {
	return l.Decisions + l.Lessons + l.References + l.Glossary + l.WorkingMemory + l.PathHistory
}

// CommonRoot is the main checkout of the repo at dir: the parent of
// `git rev-parse --git-common-dir`. Outside git it is dir.
func CommonRoot(ctx context.Context, r Runner, dir string) string {
	out, err := r.Run(ctx, dir, "git", "rev-parse", "--git-common-dir")
	common := strings.TrimSpace(string(out))
	if err != nil || common == "" {
		return dir
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(dir, common)
	}
	return filepath.Dir(common)
}

// ReadUsage returns every well-formed line of the usage log of the repo at
// dir. A missing log is no data, not an error; a malformed line is skipped.
func ReadUsage(ctx context.Context, r Runner, dir string) ([]Line, error) {
	data, err := os.ReadFile(filepath.Join(CommonRoot(ctx, r, dir), LogPath))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var lines []Line
	for raw := range bytes.SplitSeq(data, []byte("\n")) {
		var l Line
		if len(bytes.TrimSpace(raw)) == 0 || json.Unmarshal(raw, &l) != nil {
			continue
		}
		lines = append(lines, l)
	}
	return lines, nil
}

// FirstCommit returns the time of the first commit on branch since since. ok
// is false when the branch has none; an error means the branch could not be
// read (it is gone, or git failed), so the first commit is unknown. The
// remote branch is tried when the local one is missing.
func FirstCommit(ctx context.Context, r Runner, dir, branch string, since time.Time) (time.Time, bool, error) {
	var err error
	for _, ref := range []string{branch, "origin/" + branch} {
		var out []byte
		out, err = r.Run(ctx, dir, "git", "log", "--reverse", "--format=%cI",
			"--since="+since.UTC().Format(time.RFC3339), ref, "--")
		if err != nil {
			continue
		}
		first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
		if first == "" {
			return time.Time{}, false, nil
		}
		t, err := time.Parse(time.RFC3339, first)
		if err != nil {
			return time.Time{}, false, err
		}
		return t, true, nil
	}
	return time.Time{}, false, err
}
