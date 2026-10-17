// Package config loads greenhouse.toml: the repo the factory works on, the
// plan it runs, its WIP and quota caps, the deterministic verify commands and
// the model and turn budget of each LLM stage (see docs/DESIGN.md).
package config

import (
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/n8o/greenhouse/internal/stage"
)

// DefaultPath is the config file greenhouse reads when none is given.
const DefaultPath = "greenhouse.toml"

// LLMStages are the stages that run a Claude session and so take a budget.
// verify is deterministic and review is the human, so neither has one.
var LLMStages = []stage.Stage{stage.Spec, stage.Implement, stage.Learn}

// Config is the parsed greenhouse.toml.
type Config struct {
	// Repo is the repository greenhouse works on. A relative path is
	// resolved against the directory holding the config file.
	Repo string `toml:"repo"`
	// PlanPrefix selects the plan's beads: every bead whose ID is
	// "<PlanPrefix>-<n>".
	PlanPrefix string `toml:"plan_prefix"`
	// Base is the branch PRs merge into, on origin: a bead is done once its
	// close is there, and verify rebases each PR onto it. "" means the
	// remote's default branch (origin/HEAD).
	Base string `toml:"base"`
	// Prompts is a directory of stage templates that replaces the ones
	// built into greenhouse; "" uses the built-in ones. A relative path is
	// resolved like Repo.
	Prompts string           `toml:"prompts"`
	WIP     WIP              `toml:"wip"`
	Quota   Quota            `toml:"quota"`
	Verify  Verify           `toml:"verify"`
	Stages  map[string]Stage `toml:"stages"`
	Run     Run              `toml:"run"`
}

// Run configures the scheduler loop (`greenhouse tick` / `run`).
type Run struct {
	// Parent is the agent-deck session stage sessions are launched under and
	// polled through (`session children`). "" means $AGENTDECK_INSTANCE_ID,
	// the session greenhouse itself runs in.
	Parent string `toml:"parent"`
	// Interval is the pause between ticks of `greenhouse run`, as a Go
	// duration.
	Interval string `toml:"interval"`
}

// TickInterval returns Run.Interval parsed; Validate has checked it.
func (r Run) TickInterval() time.Duration {
	d, _ := time.ParseDuration(r.Interval)
	return d
}

// DefaultTimeouts is the hard wall-clock limit of each LLM stage session
// when its stages.<name>.timeout is unset. A session past its limit is
// stopped and counts as failed; max_turns is only advisory.
var DefaultTimeouts = map[stage.Stage]time.Duration{
	stage.Spec:      30 * time.Minute,
	stage.Implement: 2 * time.Hour,
	stage.Learn:     30 * time.Minute,
}

// WIP caps how much work is in flight (DESIGN.md "Gates").
type WIP struct {
	Implement int `toml:"implement"` // concurrent implement sessions
	OpenPRs   int `toml:"open_prs"`  // open agent PRs awaiting review
}

// Quota holds the launch thresholds as fractions of each usage window
// (ADR-0002).
type Quota struct {
	FiveHourMax float64 `toml:"five_hour_max"`
	WeeklyShare float64 `toml:"weekly_share"`
}

// Verify lists the deterministic checks run on a PR branch (ADR-0004).
type Verify struct {
	Commands []string `toml:"commands"`
	// Timeout bounds each command, as a Go duration ("10m"). A hung command
	// fails verify instead of holding the factory.
	Timeout string `toml:"timeout"`
}

// CommandTimeout returns Verify.Timeout parsed; Validate has checked it.
func (v Verify) CommandTimeout() time.Duration {
	d, _ := time.ParseDuration(v.Timeout)
	return d
}

// Stage is the budget of one LLM stage session. Model and Effort are pinned
// per stage: a session that leaves them out inherits the user's Claude Code
// default, which may be the most expensive model at the highest effort.
type Stage struct {
	Model    string `toml:"model"`
	Effort   string `toml:"effort"`
	MaxTurns int    `toml:"max_turns"`
	// Timeout is the session's hard wall-clock limit, as a Go duration;
	// "" means DefaultTimeouts.
	Timeout string `toml:"timeout"`
}

// Timeout returns the wall-clock limit of an st session.
func (c Config) Timeout(st stage.Stage) time.Duration {
	if d, err := time.ParseDuration(c.Stages[string(st)].Timeout); err == nil && d > 0 {
		return d
	}
	return DefaultTimeouts[st]
}

// Efforts are the reasoning efforts a Claude session accepts
// (`agent-deck launch -effort`).
var Efforts = []string{"low", "medium", "high", "xhigh", "max"}

// Default returns the config used for any key greenhouse.toml leaves out.
func Default() Config {
	return Config{
		Repo:   ".",
		WIP:    WIP{Implement: 1, OpenPRs: 3},
		Quota:  Quota{FiveHourMax: 0.8, WeeklyShare: 0.5},
		Verify: Verify{Timeout: "10m"},
		Run:    Run{Interval: "5m"},
	}
}

// Load reads the config at path over Default and validates it. Unknown keys
// are an error, so a typo cannot silently fall back to a default.
func Load(path string) (Config, error) {
	cfg, err := Read(path)
	if err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("config %s: %w", path, err)
	}
	return cfg, nil
}

// Read is Load without Validate. `greenhouse doctor` uses it to report on a
// config that would not validate, rather than stopping at the first error.
func Read(path string) (Config, error) {
	cfg := Default()
	md, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		return Config{}, fmt.Errorf("config %s: %w", path, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, len(undecoded))
		for i, k := range undecoded {
			keys[i] = k.String()
		}
		return Config{}, fmt.Errorf("config %s: unknown keys: %s", path, strings.Join(keys, ", "))
	}
	if !filepath.IsAbs(cfg.Repo) {
		cfg.Repo = filepath.Join(filepath.Dir(path), cfg.Repo)
	}
	if abs, err := filepath.Abs(cfg.Repo); err == nil {
		cfg.Repo = abs
	}
	if cfg.Prompts != "" {
		if !filepath.IsAbs(cfg.Prompts) {
			cfg.Prompts = filepath.Join(filepath.Dir(path), cfg.Prompts)
		}
		if abs, err := filepath.Abs(cfg.Prompts); err == nil {
			cfg.Prompts = abs
		}
	}
	return cfg, nil
}

// Validate reports every invalid field at once.
func (c Config) Validate() error {
	var errs []error
	if c.Repo == "" {
		errs = append(errs, errors.New("repo is required"))
	}
	if c.PlanPrefix == "" {
		errs = append(errs, errors.New("plan_prefix is required"))
	}
	if strings.HasPrefix(c.Base, "origin/") || strings.ContainsAny(c.Base, " \t") {
		errs = append(errs, fmt.Errorf("base is a branch name like \"main\", not a ref; got %q", c.Base))
	}
	if c.WIP.Implement < 1 {
		errs = append(errs, fmt.Errorf("wip.implement must be >= 1, got %d", c.WIP.Implement))
	}
	if c.WIP.OpenPRs < 1 {
		errs = append(errs, fmt.Errorf("wip.open_prs must be >= 1, got %d", c.WIP.OpenPRs))
	}
	if !isFraction(c.Quota.FiveHourMax) {
		errs = append(errs, fmt.Errorf("quota.five_hour_max must be in (0, 1], got %v", c.Quota.FiveHourMax))
	}
	if !isFraction(c.Quota.WeeklyShare) {
		errs = append(errs, fmt.Errorf("quota.weekly_share must be in (0, 1], got %v", c.Quota.WeeklyShare))
	}
	if len(c.Verify.Commands) == 0 {
		errs = append(errs, errors.New("verify.commands must list at least one command"))
	}
	if d, err := time.ParseDuration(c.Verify.Timeout); err != nil || d <= 0 {
		errs = append(errs, fmt.Errorf("verify.timeout must be a positive duration like \"10m\", got %q", c.Verify.Timeout))
	}
	if d, err := time.ParseDuration(c.Run.Interval); err != nil || d <= 0 {
		errs = append(errs, fmt.Errorf("run.interval must be a positive duration like \"5m\", got %q", c.Run.Interval))
	}
	for i, cmd := range c.Verify.Commands {
		if strings.TrimSpace(cmd) == "" {
			errs = append(errs, fmt.Errorf("verify.commands[%d] is empty", i))
		}
	}
	for _, name := range slices.Sorted(maps.Keys(c.Stages)) {
		if !slices.Contains(LLMStages, stage.Stage(name)) {
			errs = append(errs, fmt.Errorf("stages.%s: not an LLM stage (want one of %s)", name, llmStageList()))
		}
	}
	for _, st := range LLMStages {
		name := string(st)
		s, ok := c.Stages[name]
		if !ok {
			errs = append(errs, fmt.Errorf("stages.%s is required", name))
			continue
		}
		if s.Model == "" {
			errs = append(errs, fmt.Errorf("stages.%s.model is required", name))
		}
		if ManualMode(s.Model) {
			errs = append(errs, fmt.Errorf("stages.%s.model %q cannot run in auto mode; use sonnet or opus", name, s.Model))
		}
		if !slices.Contains(Efforts, s.Effort) {
			errs = append(errs, fmt.Errorf("stages.%s.effort must be one of %s, got %q", name, strings.Join(Efforts, ", "), s.Effort))
		}
		if s.MaxTurns < 1 {
			errs = append(errs, fmt.Errorf("stages.%s.max_turns must be >= 1, got %d", name, s.MaxTurns))
		}
		if d, err := time.ParseDuration(s.Timeout); s.Timeout != "" && (err != nil || d <= 0) {
			errs = append(errs, fmt.Errorf("stages.%s.timeout must be a positive duration like \"2h\", got %q", name, s.Timeout))
		}
	}
	return errors.Join(errs...)
}

// ManualMode reports whether Claude Code starts a session on model in manual
// mode (haiku), where an unattended session stalls on its first permission
// prompt.
func ManualMode(model string) bool { return strings.Contains(strings.ToLower(model), "haiku") }

func llmStageList() string {
	names := make([]string, len(LLMStages))
	for i, s := range LLMStages {
		names[i] = string(s)
	}
	return strings.Join(names, ", ")
}

func isFraction(f float64) bool { return f > 0 && f <= 1 }
