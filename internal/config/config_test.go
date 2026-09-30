package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const minimal = `
plan_prefix = "gr-boot"

[verify]
commands = ["go test ./..."]

[stages.spec]
model = "sonnet"
max_turns = 20

[stages.implement]
model = "opus"
max_turns = 80

[stages.learn]
model = "sonnet"
max_turns = 15
`

func write(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "greenhouse.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadRepoExample(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", DefaultPath))
	if err != nil {
		t.Fatalf("the committed greenhouse.toml must load: %v", err)
	}
	if cfg.PlanPrefix != "gr-boot" {
		t.Errorf("PlanPrefix = %q, want gr-boot", cfg.PlanPrefix)
	}
	if want, _ := filepath.Abs(filepath.Join("..", "..")); cfg.Repo != want {
		t.Errorf("Repo = %q, want %q", cfg.Repo, want)
	}
}

func TestLoadAppliesDefaults(t *testing.T) {
	path := write(t, minimal)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	def := Default()
	if cfg.WIP != def.WIP {
		t.Errorf("WIP = %+v, want default %+v", cfg.WIP, def.WIP)
	}
	if cfg.Quota != def.Quota {
		t.Errorf("Quota = %+v, want default %+v", cfg.Quota, def.Quota)
	}
	if cfg.Repo != filepath.Dir(path) {
		t.Errorf("Repo = %q, want the config's directory %q", cfg.Repo, filepath.Dir(path))
	}
	if got := cfg.Stages["implement"]; got != (Stage{Model: "opus", MaxTurns: 80}) {
		t.Errorf("stages.implement = %+v", got)
	}
}

func TestLoadRejects(t *testing.T) {
	tests := []struct {
		name, body, want string
	}{
		{"unknown key", minimal + "\n[wip]\nimplemnt = 2\n", "unknown keys: wip.implemnt"},
		{"no prefix", strings.Replace(minimal, `plan_prefix = "gr-boot"`, "", 1), "plan_prefix is required"},
		{"zero wip", minimal + "\n[wip]\nimplement = 0\n", "wip.implement must be >= 1"},
		{"share above 1", minimal + "\n[quota]\nweekly_share = 1.5\n", "quota.weekly_share must be in (0, 1]"},
		{"no verify", strings.Replace(minimal, `commands = ["go test ./..."]`, "commands = []", 1), "verify.commands must list"},
		{"blank verify", strings.Replace(minimal, `"go test ./..."`, `" "`, 1), "verify.commands[0] is empty"},
		{"missing stage", strings.Replace(minimal, "[stages.learn]", "[stages.other]", 1), "stages.learn is required"},
		{"not an llm stage", minimal + "\n[stages.verify]\nmodel = \"x\"\nmax_turns = 1\n", "stages.verify: not an LLM stage"},
		{"no turns", strings.Replace(minimal, "max_turns = 20", "max_turns = 0", 1), "stages.spec.max_turns must be >= 1"},
		{"bad toml", "plan_prefix = ", "config"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(write(t, tt.body))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Load error = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.toml")); err == nil {
		t.Fatal("want an error for a missing file")
	}
}
