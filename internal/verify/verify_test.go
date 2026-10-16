package verify

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/n8o/greenhouse/internal/stage"
)

const (
	closedLine = `{"id":"gr-boot-5","status":"closed","title":"V","close_reason":"this PR","closed_at":"t","started_at":"s"}` + "\n"
	openLine   = `{"id":"gr-boot-5","status":"open","title":"V"}` + "\n"
	otherLine  = `{"id":"gr-boot-6","status":"open","title":"L"}` + "\n"
)

// gitReplies fakes a branch whose plan file went from before to after.
func gitReplies(before, after string) map[string]Reply {
	r := map[string]Reply{
		"git merge-base origin/main HEAD":    {Out: "abc\n"},
		"git show abc:.beads/plans/p.jsonl":  {Out: before},
		"git show HEAD:.beads/plans/p.jsonl": {Out: after},
	}
	if before != after {
		r["git diff --name-status --no-renames abc HEAD -- .beads/plans/"] = Reply{Out: "M\t.beads/plans/p.jsonl\n"}
	}
	return r
}

func TestPlanScope(t *testing.T) {
	tests := []struct {
		name          string
		before, after string
		bad           []string // substrings, one per expected violation
	}{
		{"closes own bead", openLine + otherLine, closedLine + otherLine, nil},
		{"title edit slipped in", openLine, strings.Replace(closedLine, `"V"`, `"W"`, 1), []string{"gr-boot-5: plan fields changed: title"}},
		{"dependency added", openLine, strings.Replace(closedLine, `"V"`, `"V","dependencies":[1]`, 1), []string{"plan fields changed: dependencies"}},
		{"touches another bead", openLine + otherLine, closedLine + strings.Replace(otherLine, "open", "closed", 1), []string{"gr-boot-6: changed"}},
		{"adds a bead", openLine, closedLine + `{"id":"gr-boot-9","status":"open"}` + "\n", []string{"gr-boot-9: added"}},
		{"removes a bead", openLine + otherLine, closedLine, []string{"gr-boot-6: removed"}},
		{"own bead edited but not closed", openLine, strings.Replace(openLine, `"V"`, `"W"`, 1), []string{"gr-boot-5: changed without closing"}},
		{"own bead untouched", openLine + otherLine, openLine + otherLine, []string{"gr-boot-5: not closed by this PR"}},
		{"reopens a closed bead", closedLine, openLine, []string{"gr-boot-5: was already closed"}},
		{"unparseable line", openLine, closedLine + "{oops\n", []string{"unparseable line"}},
		{"own bead records its spec", openLine + otherLine, strings.Replace(closedLine, `"V"`, `"V","acceptance_criteria":"- go test passes","design":"scope.go: allow spec","notes":"n"`, 1) + otherLine, nil},
		{"own bead's spec revised", strings.Replace(openLine, `"V"`, `"V","design":"old"`, 1), strings.Replace(closedLine, `"V"`, `"V","design":"new"`, 1), nil},
		{"spec without closing", openLine, strings.Replace(openLine, `"V"`, `"V","design":"d"`, 1), []string{"gr-boot-5: changed without closing"}},
		{"another bead's spec", openLine + otherLine, closedLine + strings.Replace(otherLine, `"L"`, `"L","acceptance_criteria":"a","design":"d"`, 1), []string{"gr-boot-6: changed (only gr-boot-5 may change)"}},
		{"spec plus title", openLine, strings.Replace(closedLine, `"V"`, `"W","design":"d"`, 1), []string{"gr-boot-5: plan fields changed: title"}},
		{"spec plus priority", openLine, strings.Replace(closedLine, `"V"`, `"V","design":"d","priority":1`, 1), []string{"gr-boot-5: plan fields changed: priority"}},
		{"spec plus description", openLine, strings.Replace(closedLine, `"V"`, `"V","notes":"n","description":"x"`, 1), []string{"gr-boot-5: plan fields changed: description"}},
		{"names every offender", openLine + otherLine, closedLine + strings.Replace(otherLine, "open", "closed", 1) + `{"id":"gr-boot-9"}` + "\n", []string{"gr-boot-6", "gr-boot-9"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &Fake{Replies: gitReplies(tt.before, tt.after)}
			bad, err := CheckPlanScope(context.Background(), f, "wt", "origin/main", "gr-boot-5")
			if err != nil {
				t.Fatal(err)
			}
			if len(bad) != len(tt.bad) {
				t.Fatalf("violations = %q, want %d like %q", bad, len(tt.bad), tt.bad)
			}
			for i, want := range tt.bad {
				if !strings.Contains(bad[i], want) {
					t.Errorf("violation %d = %q, want it to contain %q", i, bad[i], want)
				}
			}
		})
	}
}

func TestPlanScopeFileAddedAndDeleted(t *testing.T) {
	f := &Fake{Replies: map[string]Reply{
		"git merge-base origin/main HEAD":                               {Out: "abc\n"},
		"git diff --name-status --no-renames abc HEAD -- .beads/plans/": {Out: "D\t.beads/plans/p.jsonl\nA\t.beads/plans/q.jsonl\n"},
		"git show abc:.beads/plans/p.jsonl":                             {Out: otherLine},
		"git show HEAD:.beads/plans/q.jsonl":                            {Out: closedLine},
	}}
	bad, err := CheckPlanScope(context.Background(), f, "wt", "origin/main", "gr-boot-5")
	if err != nil || len(bad) != 2 || !strings.Contains(bad[0], "gr-boot-6: removed") || !strings.Contains(bad[1], "gr-boot-5: added") {
		t.Errorf("bad = %q, err = %v", bad, err)
	}
}

func TestPlanScopeGitError(t *testing.T) {
	f := &Fake{Replies: map[string]Reply{"git merge-base origin/main HEAD": {Err: errors.New("exit status 128"), Out: "fatal: no merge base"}}}
	_, err := CheckPlanScope(context.Background(), f, "wt", "origin/main", "gr-boot-5")
	if err == nil || !strings.Contains(err.Error(), "no merge base") {
		t.Errorf("err = %v", err)
	}
}

func opts(cmds ...string) Options {
	return Options{Dir: "wt", Base: "origin/main", Bead: "gr-boot-5", Commands: cmds, Timeout: time.Second}
}

func TestRun(t *testing.T) {
	ctx := context.Background()
	t.Run("all pass", func(t *testing.T) {
		f := &Fake{Replies: gitReplies(openLine, closedLine)}
		res, err := Run(ctx, f, opts("a", "b"))
		if err != nil || !res.OK() || len(res.Checks) != 3 || res.Log != "" {
			t.Fatalf("res = %+v, err = %v", res, err)
		}
		if got := res.Event(7, 0); got != (stage.Event{Kind: stage.VerifyPass, PR: 7}) {
			t.Errorf("event = %+v", got)
		}
	})
	t.Run("stops at the first failure", func(t *testing.T) {
		f := &Fake{Replies: gitReplies(openLine, closedLine)}
		f.Replies["sh -c a"] = Reply{Out: "boom\n", Err: errors.New("exit status 2")}
		res, err := Run(ctx, f, opts("ok", "a", "never"))
		if err != nil || res.OK() {
			t.Fatalf("res = %+v, err = %v", res, err)
		}
		if strings.Contains(strings.Join(f.Calls, "|"), "never") {
			t.Errorf("ran a command after the failure: %q", f.Calls)
		}
		for _, want := range []string{"$ a", "exit status 2", "boom"} {
			if !strings.Contains(res.Log, want) {
				t.Errorf("log %q lacks %q", res.Log, want)
			}
		}
		ev := res.Event(7, 1)
		if ev.Kind != stage.VerifyFail || ev.Reason != res.Log || ev.Retries != 1 {
			t.Errorf("event = %+v", ev)
		}
		if _, _, err := stage.Next(stage.Verify, ev); err != nil {
			t.Errorf("stage rejects the event: %v", err)
		}
	})
	t.Run("plan scope fails before any command runs", func(t *testing.T) {
		f := &Fake{Replies: gitReplies(openLine+otherLine, closedLine+strings.Replace(otherLine, "open", "closed", 1))}
		res, err := Run(ctx, f, opts("a"))
		if err != nil || res.OK() || !strings.Contains(res.Log, "gr-boot-6") || len(res.Checks) != 1 {
			t.Fatalf("res = %+v, err = %v", res, err)
		}
		if strings.Contains(strings.Join(f.Calls, "|"), "sh -c") {
			t.Errorf("commands ran: %q", f.Calls)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		f := &Fake{Replies: gitReplies(openLine, closedLine)}
		f.Replies["sh -c hang"] = Reply{Block: true, Out: "partial"}
		o := opts("hang")
		o.Timeout = 20 * time.Millisecond
		res, err := Run(ctx, f, o)
		if err != nil || res.OK() || !strings.Contains(res.Log, "timed out after 20ms") || !strings.Contains(res.Log, "partial") {
			t.Fatalf("res = %+v, err = %v", res, err)
		}
	})
	t.Run("cancelled is an error, not a failure", func(t *testing.T) {
		c, cancel := context.WithCancel(ctx)
		cancel()
		f := &Fake{Replies: gitReplies(openLine, closedLine)}
		f.Replies["sh -c a"] = Reply{Err: errors.New("killed")}
		if _, err := Run(c, f, opts("a")); !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("git unavailable", func(t *testing.T) {
		f := &Fake{Replies: map[string]Reply{"git merge-base origin/main HEAD": {Err: errors.New("x")}}}
		if _, err := Run(ctx, f, opts("a")); err == nil {
			t.Error("want an error")
		}
	})
}

func TestBound(t *testing.T) {
	long := strings.Repeat("a", 5000) + "MIDDLE" + strings.Repeat("z", 5000)
	for _, max := range []int{200, 1000, MaxLogBytes} {
		got := bound("$ cmd\n", long, max)
		if len(got) > max {
			t.Errorf("max %d: len %d", max, len(got))
		}
		if !strings.HasPrefix(got, "$ cmd\na") || !strings.HasSuffix(got, "z") || !strings.Contains(got, "bytes elided") {
			t.Errorf("max %d: want header, head, marker, tail: %.60q ... %.60q", max, got, got[len(got)-30:])
		}
	}
	if got := bound("h\n", "short", 100); got != "h\nshort" {
		t.Errorf("short output changed: %q", got)
	}
	if got := bound(strings.Repeat("h", 50), "out", 10); len(got) != 10 {
		t.Errorf("header alone over max: len %d", len(got))
	}
	utf := strings.Repeat("é", 3000)
	if got := bound("", utf, 301); !strings.HasSuffix(got, "é") || strings.ContainsRune(got, '�') {
		t.Errorf("cut a rune in half: %q", got)
	}
}

func TestPrint(t *testing.T) {
	var b bytes.Buffer
	Print(&b, Result{Checks: []Check{{Name: PlanScopeName, OK: true, Detail: "fine"}, {Name: "go test ./...", Detail: "exit status 1"}}, Log: "LOG"})
	for _, want := range []string{"PASS  plan-scope     fine", "FAIL  go test ./...  exit status 1", "LOG"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("output %q lacks %q", b.String(), want)
		}
	}
}

func TestExec(t *testing.T) {
	out, err := Exec{}.Run(context.Background(), t.TempDir(), "sh", "-c", "echo hi; echo err >&2; exit 3")
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 3 || !strings.Contains(string(out), "hi") || !strings.Contains(string(out), "err") {
		t.Errorf("out %q err %v", out, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = Exec{}.Run(ctx, t.TempDir(), "sh", "-c", "sleep 30 & sleep 30")
	if err == nil || time.Since(start) > 10*time.Second {
		t.Errorf("a hung command was not killed: err %v after %v", err, time.Since(start))
	}
}
