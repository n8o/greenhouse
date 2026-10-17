package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/n8o/greenhouse/internal/config"
	"github.com/n8o/greenhouse/internal/verify"
)

const symref = "git symbolic-ref --short refs/remotes/origin/HEAD"

func TestBaseBranch(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name  string
		base  string
		reply verify.Reply
		want  string
		err   string
	}{
		{"configured wins", "develop", verify.Reply{Out: "origin/master\n"}, "develop", ""},
		{"origin default", "", verify.Reply{Out: "origin/master\n"}, "master", ""},
		{"no origin/HEAD", "", verify.Reply{Err: errors.New("exit status 128")}, "", "set base"},
		{"not an origin ref", "", verify.Reply{Out: "upstream/main\n"}, "", "set base"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &verify.Fake{Replies: map[string]verify.Reply{symref: tt.reply}}
			got, err := baseBranch(ctx, r, config.Config{Repo: "/r", Base: tt.base})
			if got != tt.want || (tt.err == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tt.err)) {
				t.Errorf("got %q, %v; want %q, error ~%q", got, err, tt.want, tt.err)
			}
		})
	}
}

// TestSchedulerUsesBase: the base names every ref the scheduler reads and
// rebases onto, so a repo on master is read on origin/master.
func TestSchedulerUsesBase(t *testing.T) {
	w := newWorld(t)
	cfg := w.s.cfg
	cfg.Base = "master"
	s, err := newScheduler(context.Background(), cfg, w.s.adapters, &w.out, true)
	if err != nil {
		t.Fatal(err)
	}
	w.runner.Replies["git ls-tree --name-only origin/master -- .beads/plans/"] = verify.Reply{Out: ".beads/plans/p.jsonl\n"}
	w.runner.Replies["git show origin/master:.beads/plans/p.jsonl"] = verify.Reply{Out: `{"id":"p-1","status":"open"}` + "\n"}
	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v\n%s", err, w.out.String())
	}
	if !strings.Contains(strings.Join(w.runner.Calls, "\n"), "git ls-tree --name-only origin/master") {
		t.Errorf("the plan was not read on origin/master: %v", w.runner.Calls)
	}
}

func gitInit(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	return dir
}

// TestExcludeState: a repo that does not ignore .greenhouse/ gets it in its
// clone-local info/exclude, once; no tracked file changes.
func TestExcludeState(t *testing.T) {
	repo := gitInit(t)
	for range 2 {
		if err := excludeState(context.Background(), verify.Exec{}, repo); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(b), "/.greenhouse/\n"); n != 1 {
		t.Errorf("info/exclude lists .greenhouse/ %d times:\n%s", n, b)
	}
	os.MkdirAll(filepath.Join(repo, ".greenhouse"), 0o755)
	os.WriteFile(filepath.Join(repo, ".greenhouse", "ledger.jsonl"), []byte("{}\n"), 0o644)
	out, _ := exec.Command("git", "-C", repo, "status", "--porcelain").Output()
	if len(out) > 0 {
		t.Errorf("git status shows run state: %s", out)
	}
	if _, err := os.Stat(filepath.Join(repo, ".gitignore")); err == nil {
		t.Error("excludeState wrote a .gitignore")
	}
}

func TestInit(t *testing.T) {
	ctx := context.Background()
	repo := gitInit(t)
	path := filepath.Join(t.TempDir(), "configs", "app.toml") // init makes the directory
	r := &verify.Fake{Replies: map[string]verify.Reply{symref: {Out: "origin/master\n"}}}
	var stdout, stderr bytes.Buffer
	if code := runInit(ctx, []string{"-repo", repo, "-plan", "x-plan", "-config", path}, &stdout, &stderr, r); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("the starter config does not load: %v", err)
	}
	if cfg.Repo != repo || cfg.PlanPrefix != "x-plan" || cfg.Base != "master" {
		t.Errorf("config %+v", cfg)
	}
	if want := "nugit pr-render -base master -head HEAD -fail-on fail"; len(cfg.Verify.Commands) != 1 || cfg.Verify.Commands[0] != want {
		t.Errorf("verify commands %q, want [%q]", cfg.Verify.Commands, want)
	}
	if code := runInit(ctx, []string{"-repo", repo, "-plan", "x-plan", "-config", path}, &stdout, &stderr, r); code == 0 {
		t.Error("init overwrote an existing config without -force")
	}
	if code := runInit(ctx, []string{"-repo", repo, "-config", path, "-force"}, &stdout, &stderr, r); code != 2 {
		t.Errorf("init without -plan: exit %d, want 2", code)
	}
	noHead := &verify.Fake{Replies: map[string]verify.Reply{symref: {Err: errors.New("exit status 128")}}}
	stderr.Reset()
	if code := runInit(ctx, []string{"-repo", repo, "-plan", "x", "-config", path, "-force"}, &stdout, &stderr, noHead); code == 0 || !strings.Contains(stderr.String(), "-base") {
		t.Errorf("init with no origin/HEAD: exit %d, stderr %q", code, stderr.String())
	}
}

func TestRelRepo(t *testing.T) {
	tmp := t.TempDir()
	for _, tt := range []struct{ config, repo, want string }{
		{filepath.Join(tmp, "repo", "greenhouse.toml"), filepath.Join(tmp, "repo"), "."},
		{filepath.Join(tmp, "greenhouse.toml"), filepath.Join(tmp, "repo"), "repo"},
		{filepath.Join(tmp, "elsewhere", "g.toml"), filepath.Join(tmp, "repo"), filepath.Join(tmp, "repo")},
	} {
		if got := relRepo(tt.config, tt.repo); got != tt.want {
			t.Errorf("relRepo(%q, %q) = %q, want %q", tt.config, tt.repo, got, tt.want)
		}
	}
}

func TestVersion(t *testing.T) {
	var stdout bytes.Buffer
	if code := run(context.Background(), []string{"version"}, &stdout, &stdout, deps{}); code != 0 || !strings.HasPrefix(stdout.String(), "greenhouse ") {
		t.Errorf("exit %d: %q", code, stdout.String())
	}
}
