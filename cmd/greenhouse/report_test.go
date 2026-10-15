package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/n8o/greenhouse/internal/ledger"
	"github.com/n8o/greenhouse/internal/memory"
	"github.com/n8o/greenhouse/internal/report"
	"github.com/n8o/greenhouse/internal/verify"
)

// reportRepo writes this repo's greenhouse.toml into a temp dir, so the
// config's repo is that dir, and returns the config path and the repo.
func reportRepo(t *testing.T) (string, string) {
	t.Helper()
	data, err := os.ReadFile(repoConfig)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "greenhouse.toml")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path, dir
}

func TestReport(t *testing.T) {
	path, dir := reportRepo(t)
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	five := func(v float64) *float64 { return &v }
	st := ledger.Open(dir)
	for _, r := range []ledger.Record{
		{Bead: "gr-1", Stage: "implement", Session: "s1", Start: t0, End: t0.Add(30 * time.Minute), Outcome: "implement-done", PR: 9,
			Before: ledger.Quota{FiveHour: five(10), SevenDay: five(20)}, After: ledger.Quota{FiveHour: five(16), SevenDay: five(22)}},
		{Bead: "gr-1", Stage: "verify", Start: t0.Add(30 * time.Minute), End: t0.Add(31 * time.Minute), Outcome: "verify-pass", PR: 9},
		{Bead: "gr-1", Stage: "review", Start: t0.Add(31 * time.Minute), End: t0.Add(2 * time.Hour), Outcome: "merged", PR: 9},
	} {
		if err := st.Append(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, ".nugit", ".cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	usage := `{"time":"2026-10-01T12:05:00Z","source":"cli","branch":"feature/gr-1-implement","path":"x.go","component":"cli","decisions":2}
{"time":"2026-10-01T12:20:00Z","source":"cli","agent_session":"s1","path":"y.go","component":""}
`
	if err := os.WriteFile(filepath.Join(dir, memory.LogPath), []byte(usage), 0o644); err != nil {
		t.Fatal(err)
	}
	git := &verify.Fake{Replies: map[string]verify.Reply{
		"git rev-parse --git-common-dir": {Out: filepath.Join(dir, ".git")},
		"git log --reverse --format=%cI --since=2026-10-01T12:00:00Z feature/gr-1-implement --": {Out: "2026-10-01T12:10:00Z\n"},
	}}

	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"report", "-config", path}, &stdout, &stderr, deps{git: git}); code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, stderr.String())
	}
	for _, want := range []string{
		"ledger: 3 runs over 1 beads",
		"#9    gr-1  6.0     2.0     2",
		"gr-1  merged  2h0m0s",
		"scoped memory: 1 of 1 LLM runs loaded context · 1 context calls (1 unattributed)",
		"gr-1  implement  s1       session  1      0              1      1",
		"too few runs to compare (need 5 per group)",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("output missing %q:\n%s", want, stdout.String())
		}
	}

	stdout.Reset()
	if code := run(context.Background(), []string{"report", "-config", path, "--json", "-min-sample", "1"}, &stdout, &stderr, deps{git: git}); code != 0 {
		t.Fatalf("--json exit %d, stderr: %s", code, stderr.String())
	}
	var rep report.Report
	if err := json.Unmarshal(stdout.Bytes(), &rep); err != nil {
		t.Fatalf("--json output: %v\n%s", err, stdout.String())
	}
	if rep.Runs != 3 || len(rep.Quota.Merged) != 1 || rep.Comparison.MinSample != 1 || rep.Comparison.Comparable {
		t.Errorf("report = %+v", rep) // the none group is empty, so still not comparable
	}
}

func TestReportEmptyLedger(t *testing.T) {
	path, _ := reportRepo(t)
	git := &verify.Fake{Replies: map[string]verify.Reply{"git rev-parse --git-common-dir": {Err: errors.New("not a repo")}}}
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"report", "-config", path}, &stdout, &stderr, deps{git: git}); code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, stderr.String())
	}
	for _, want := range []string{"ledger: 0 runs over 0 beads", "quota per merged PR: 0 merged", "rejects: 0",
		"loaded n=0 · none n=0", "too few runs to compare"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("output missing %q:\n%s", want, stdout.String())
		}
	}
	stdout.Reset()
	if code := run(context.Background(), []string{"report", "-config", path, "--json"}, &stdout, &stderr, deps{git: git}); code != 0 {
		t.Fatalf("--json exit %d", code)
	}
	if strings.Contains(stdout.String(), "null") || !strings.Contains(stdout.String(), `"comparable": false`) {
		t.Errorf("--json on an empty ledger:\n%s", stdout.String())
	}
}

func TestReportErrors(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		code int
		want string
	}{
		{"stray arg", []string{"report", "extra"}, 2, "unexpected arguments"},
		{"bad flag", []string{"report", "-nope"}, 2, "flag provided but not defined"},
		{"bad min sample", []string{"report", "-min-sample", "0"}, 2, "-min-sample"},
		{"missing config", []string{"report", "-config", filepath.Join(t.TempDir(), "none.toml")}, 1, "none.toml"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(context.Background(), tt.args, &stdout, &stderr, deps{git: &verify.Fake{}}); code != tt.code {
				t.Errorf("exit %d, want %d", code, tt.code)
			}
			if !strings.Contains(stderr.String(), tt.want) {
				t.Errorf("stderr %q missing %q", stderr.String(), tt.want)
			}
		})
	}
}

func TestUsageListsReport(t *testing.T) {
	var stdout bytes.Buffer
	run(context.Background(), []string{"help"}, &stdout, &stdout, deps{})
	if !strings.Contains(stdout.String(), "  report ") {
		t.Errorf("usage does not list report:\n%s", stdout.String())
	}
}
