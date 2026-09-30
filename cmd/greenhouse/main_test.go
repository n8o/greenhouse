package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/n8o/greenhouse/internal/config"
	"github.com/n8o/greenhouse/internal/plan"
)

var repoConfig = filepath.Join("..", "..", config.DefaultPath)

func fakeSource(f *plan.Fake, gotCfg *config.Config) newSource {
	return func(cfg config.Config) plan.Source {
		*gotCfg = cfg
		return f
	}
}

func TestStatus(t *testing.T) {
	fake := &plan.Fake{Beads: []plan.Bead{
		{ID: "gr-boot-1", Title: "Skeleton: config + status", Status: "in_progress"},
		{ID: "gr-boot-2", Title: "Stage state machine (pure)", Status: "open", Labels: []string{"factory:approved", "stage:spec"}},
	}}
	var cfg config.Config
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"status", "-config", repoConfig}, &stdout, &stderr, fakeSource(fake, &cfg))
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, stderr.String())
	}
	if cfg.PlanPrefix != "gr-boot" {
		t.Errorf("source built from config with prefix %q", cfg.PlanPrefix)
	}
	out := stdout.String()
	for _, want := range []string{
		"plan gr-boot in ",
		": 2 beads",
		"wip implement<=1 open_prs<=3 · quota 5h<0.80 7d<0.50",
		"BEAD       STATUS       STAGE  TITLE",
		"gr-boot-1  in_progress  -      Skeleton: config + status",
		"gr-boot-2  open         spec   Stage state machine (pure)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestStatusErrors(t *testing.T) {
	var cfg config.Config
	tests := []struct {
		name string
		args []string
		src  *plan.Fake
		code int
		want string
	}{
		{"missing config", []string{"status", "-config", filepath.Join(t.TempDir(), "none.toml")}, &plan.Fake{}, 1, "none.toml"},
		{"source fails", []string{"status", "-config", repoConfig}, &plan.Fake{Err: errors.New("bd down")}, 1, "bd down"},
		{"stray arg", []string{"status", "extra"}, &plan.Fake{}, 2, "unexpected arguments"},
		{"no command", nil, &plan.Fake{}, 2, "usage:"},
		{"unknown command", []string{"tick"}, &plan.Fake{}, 2, `unknown command "tick"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(context.Background(), tt.args, &stdout, &stderr, fakeSource(tt.src, &cfg))
			if code != tt.code || !strings.Contains(stderr.String(), tt.want) {
				t.Errorf("exit %d stderr %q, want exit %d containing %q", code, stderr.String(), tt.code, tt.want)
			}
		})
	}
}
