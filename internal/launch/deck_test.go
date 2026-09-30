package launch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fixture returns a recorded agent-deck -json reply from testdata.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// scripted is a CLI whose agent-deck replies with out and err, recording args.
func scripted(out []byte, err error, got *[]string) *CLI {
	return &CLI{run: func(_ context.Context, args ...string) ([]byte, error) {
		*got = args
		return out, err
	}}
}

func TestLaunchArgsPinModelAndEffort(t *testing.T) {
	a := LaunchArgs{Repo: "/r", Branch: "feature/x-1-spec", NewBranch: true, Title: "gh-spec-x-1",
		Group: "greenhouse", Model: "sonnet", Effort: "medium", MessageFile: "/tmp/p.md"}
	want := []string{"launch", "/r", "-json", "-t", "gh-spec-x-1", "-c", "claude", "-g", "greenhouse",
		"-worktree", "feature/x-1-spec", "-b", "-no-parent",
		"-model", "sonnet", "-effort", "medium", "-auto-mode", "-assert-done", "-message-file", "/tmp/p.md"}
	if got := a.Args(); !slices.Equal(got, want) {
		t.Errorf("Args =\n %q\nwant\n %q", got, want)
	}

	a.Parent, a.NewBranch = "sched", false
	got := strings.Join(a.Args(), " ")
	if !strings.Contains(got, " -p sched ") || strings.Contains(got, "-no-parent") || strings.Contains(got, " -b ") {
		t.Errorf("with a parent and an existing branch, Args = %q", got)
	}
}

func TestCLIParsesJSON(t *testing.T) {
	ctx := context.Background()
	var args []string

	s, err := scripted(fixture(t, "launch.json"), nil, &args).Launch(ctx, LaunchArgs{Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if want := (Session{ID: "30cbb421-1", Title: "gh-spec-x-1", ClaudeSessionID: "c-1", Model: "claude-haiku-4-5",
		Effort: "low", Path: "/work/repo", Status: "starting"}); s != want {
		t.Errorf("Launch = %+v, want %+v", s, want)
	}

	st, err := scripted(fixture(t, "show.json"), nil, &args).Show(ctx, "30cbb421-1")
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != "waiting" || st.Substate != "idle-at-empty-prompt" || st.ClaudeSessionID != "c-1" || st.Busy() {
		t.Errorf("Show = %+v", st)
	}
	if want := []string{"session", "show", "30cbb421-1", "-json"}; !slices.Equal(args, want) {
		t.Errorf("show args = %q", args)
	}

	out, err := scripted(fixture(t, "output.json"), nil, &args).Output(ctx, "30cbb421-1")
	if err != nil {
		t.Fatal(err)
	}
	if out.ClaudeSessionID != "c-1" || !strings.HasPrefix(out.Content, "PONG\n"+Sentinel) {
		t.Errorf("Output = %+v", out)
	}

	kids, err := scripted(fixture(t, "children.json"), nil, &args).Children(ctx, "366869e2-1")
	if err != nil {
		t.Fatal(err)
	}
	if want := []Child{{ID: "01cdfda8-1", Title: "gh-spec-x-1", Status: "waiting"}}; !slices.Equal(kids, want) {
		t.Errorf("Children = %+v", kids)
	}

	if err := scripted(nil, nil, &args).Send(ctx, "s1", "/tmp/p.md"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"session", "send", "s1", "-message-file", "/tmp/p.md", "-q"}; !slices.Equal(args, want) {
		t.Errorf("send args = %q", args)
	}
}

func TestCLIErrors(t *testing.T) {
	ctx := context.Background()
	var args []string
	// agent-deck exits 1 and explains on stdout.
	refusal := []byte(`{"code":"INVALID_OPERATION","error":"cannot create sub-session of a sub-session (single level only)","success":false}`)
	_, err := scripted(refusal, errors.New("exit status 1"), &args).Launch(ctx, LaunchArgs{})
	if err == nil || !strings.Contains(err.Error(), "single level only") {
		t.Errorf("refused launch error = %v", err)
	}
	if _, err := scripted([]byte(`{"success":true}`), nil, &args).Launch(ctx, LaunchArgs{}); err == nil {
		t.Error("launch without a session id must fail")
	}
	if _, err := scripted([]byte("not json"), nil, &args).Show(ctx, "s1"); err == nil {
		t.Error("unparseable show must fail")
	}
	if _, err := scripted(nil, errors.New("boom"), &args).Output(ctx, "s1"); err == nil {
		t.Error("a failed command must fail")
	}
}
