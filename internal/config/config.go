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

	"github.com/BurntSushi/toml"
)

// DefaultPath is the config file greenhouse reads when none is given.
const DefaultPath = "greenhouse.toml"

// LLMStages are the stages that run a Claude session and so take a budget.
// verify is deterministic and review is the human, so neither has one.
var LLMStages = []string{"spec", "implement", "learn"}

// Config is the parsed greenhouse.toml.
type Config struct {
	// Repo is the repository greenhouse works on. A relative path is
	// resolved against the directory holding the config file.
	Repo string `toml:"repo"`
	// PlanPrefix selects the plan's beads: every bead whose ID is
	// "<PlanPrefix>-<n>".
	PlanPrefix string           `toml:"plan_prefix"`
	WIP        WIP              `toml:"wip"`
	Quota      Quota            `toml:"quota"`
	Verify     Verify           `toml:"verify"`
	Stages     map[string]Stage `toml:"stages"`
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
}

// Stage is the budget of one LLM stage session.
type Stage struct {
	Model    string `toml:"model"`
	MaxTurns int    `toml:"max_turns"`
}

// Default returns the config used for any key greenhouse.toml leaves out.
func Default() Config {
	return Config{
		Repo:  ".",
		WIP:   WIP{Implement: 1, OpenPRs: 3},
		Quota: Quota{FiveHourMax: 0.8, WeeklyShare: 0.5},
	}
}

// Load reads the config at path over Default and validates it. Unknown keys
// are an error, so a typo cannot silently fall back to a default.
func Load(path string) (Config, error) {
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
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("config %s: %w", path, err)
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
	for i, cmd := range c.Verify.Commands {
		if strings.TrimSpace(cmd) == "" {
			errs = append(errs, fmt.Errorf("verify.commands[%d] is empty", i))
		}
	}
	for _, name := range slices.Sorted(maps.Keys(c.Stages)) {
		if !slices.Contains(LLMStages, name) {
			errs = append(errs, fmt.Errorf("stages.%s: not an LLM stage (want one of %s)", name, strings.Join(LLMStages, ", ")))
		}
	}
	for _, name := range LLMStages {
		s, ok := c.Stages[name]
		if !ok {
			errs = append(errs, fmt.Errorf("stages.%s is required", name))
			continue
		}
		if s.Model == "" {
			errs = append(errs, fmt.Errorf("stages.%s.model is required", name))
		}
		if s.MaxTurns < 1 {
			errs = append(errs, fmt.Errorf("stages.%s.max_turns must be >= 1, got %d", name, s.MaxTurns))
		}
	}
	return errors.Join(errs...)
}

func isFraction(f float64) bool { return f > 0 && f <= 1 }
