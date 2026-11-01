// Package config loads and saves ~/.config/tokenmanager/config.yaml.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// AgentConfig controls one adapter.
type AgentConfig struct {
	Enabled *bool    `yaml:"enabled,omitempty"`
	Paths   []string `yaml:"paths,omitempty"` // overrides auto-detected locations
}

// IsEnabled defaults to true.
func (a AgentConfig) IsEnabled() bool { return a.Enabled == nil || *a.Enabled }

// ModelPrice is USD per million tokens. Zero cache prices fall back to the
// standard multipliers of the input price (read 0.1x, 5m write 1.25x, 1h write 2x).
type ModelPrice struct {
	Input        float64 `yaml:"input"`
	Output       float64 `yaml:"output"`
	CacheRead    float64 `yaml:"cache_read,omitempty"`
	CacheWrite5m float64 `yaml:"cache_write_5m,omitempty"`
	CacheWrite1h float64 `yaml:"cache_write_1h,omitempty"`
}

// Budget is a spending limit in the configured currency.
type Budget struct {
	Daily   float64 `yaml:"daily,omitempty"`
	Weekly  float64 `yaml:"weekly,omitempty"`
	Monthly float64 `yaml:"monthly,omitempty"`
}

// Budgets groups global and scoped budgets.
type Budgets struct {
	Global     Budget            `yaml:"global"`
	PerAgent   map[string]Budget `yaml:"per_agent,omitempty"`
	PerProject map[string]Budget `yaml:"per_project,omitempty"`
	PerModel   map[string]Budget `yaml:"per_model,omitempty"`
	// Thresholds (fractions) at which alerts fire.
	Thresholds []float64 `yaml:"thresholds,omitempty"`
}

// Notifications controls desktop alerts sent by the daemon.
type Notifications struct {
	Enabled bool `yaml:"enabled"`
}

// Privacy controls what is persisted.
type Privacy struct {
	// HideProjectPaths stores only the last path element of project directories.
	HideProjectPaths bool `yaml:"hide_project_paths"`
}

// Plan describes one provider family's subscription windows. Zero values
// fall back to the defaults in Plans.For.
type Plan struct {
	Plan          string   `yaml:"plan,omitempty"`           // label only, e.g. "max-5x"
	SessionTokens int64    `yaml:"session_tokens,omitempty"` // 0 = not configured
	WeeklyTokens  int64    `yaml:"weekly_tokens,omitempty"`
	WeeklyReset   string   `yaml:"weekly_reset,omitempty"`  // "mon 09:00"; empty = rolling 7 days
	SessionHours  float64  `yaml:"session_hours,omitempty"` // default 5
	FloorToHour   *bool    `yaml:"floor_to_hour,omitempty"` // default true
	Agents        []string `yaml:"agents,omitempty"`        // agents billed to this plan
}

// Plans configures subscription usage windows (Claude Pro/Max, ChatGPT
// Plus/Pro). Like budgets they are monitoring only.
type Plans struct {
	Anthropic Plan     `yaml:"anthropic,omitempty"`
	OpenAI    Plan     `yaml:"openai,omitempty"`
	Count     []string `yaml:"count,omitempty"`       // input, output, cache_read, cache_write
	BurnRate  Duration `yaml:"burn_window,omitempty"` // default 15m
	Alerts    bool     `yaml:"alerts,omitempty"`      // daemon notifications at budgets.thresholds
}

// PlanFamilies are the provider families that have subscription plans.
var PlanFamilies = []string{"anthropic", "openai"}

var defaultPlanAgents = map[string][]string{"anthropic": {"claude-code"}, "openai": {"codex"}}

// For returns the plan for a provider family with defaults applied.
func (p Plans) For(family string) Plan {
	var pl Plan
	switch family {
	case "anthropic":
		pl = p.Anthropic
	case "openai":
		pl = p.OpenAI
	}
	if pl.SessionHours <= 0 {
		pl.SessionHours = 5
	}
	if pl.FloorToHour == nil {
		t := true
		pl.FloorToHour = &t
	}
	if len(pl.Agents) == 0 {
		pl.Agents = defaultPlanAgents[family]
	}
	return pl
}

// CountFields is the token fields that count toward limits (default input+output).
func (p Plans) CountFields() []string {
	if len(p.Count) == 0 {
		return []string{"input", "output"}
	}
	return p.Count
}

// BurnWindow is how far back the burn rate looks (default 15 minutes).
func (p Plans) BurnWindow() time.Duration {
	if p.BurnRate.Duration <= 0 {
		return 15 * time.Minute
	}
	return p.BurnRate.Duration
}

// Config is the full user configuration.
type Config struct {
	Currency        string                 `yaml:"currency"`
	CurrencyRate    float64                `yaml:"currency_rate"` // multiplier from USD
	RefreshInterval Duration               `yaml:"refresh_interval"`
	RetentionDays   int                    `yaml:"retention_days"` // 0 keeps everything
	Agents          map[string]AgentConfig `yaml:"agents"`
	Pricing         map[string]ModelPrice  `yaml:"pricing,omitempty"` // overrides / additions
	Budgets         Budgets                `yaml:"budgets"`
	Plans           Plans                  `yaml:"plans,omitempty"`
	Notifications   Notifications          `yaml:"notifications"`
	Privacy         Privacy                `yaml:"privacy"`
	DatabasePath    string                 `yaml:"database_path,omitempty"`
}

// Duration marshals as a Go duration string ("5s").
type Duration struct{ time.Duration }

func (d Duration) MarshalYAML() (any, error) { return d.String(), nil }

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := time.ParseDuration(n.Value)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", n.Value, err)
	}
	d.Duration = v
	return nil
}

// Default returns the configuration used when no file exists.
func Default() *Config {
	return &Config{
		Currency:        "USD",
		CurrencyRate:    1,
		RefreshInterval: Duration{5 * time.Second},
		RetentionDays:   0,
		Agents:          map[string]AgentConfig{},
		Budgets:         Budgets{Thresholds: []float64{0.5, 0.8, 1.0}},
		Notifications:   Notifications{Enabled: true},
	}
}

// Dir is the configuration directory, honoring XDG_CONFIG_HOME.
func Dir() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "tokenmanager")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "tokenmanager")
}

// DataDir holds the database, pid file and daemon log.
func DataDir() string {
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, "tokenmanager")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "tokenmanager")
}

// Path is the config file location (TOKENMANAGER_CONFIG overrides).
func Path() string {
	if p := os.Getenv("TOKENMANAGER_CONFIG"); p != "" {
		return p
	}
	return filepath.Join(Dir(), "config.yaml")
}

// Load reads the config file, returning defaults when it does not exist.
func Load() (*Config, error) {
	c := Default()
	data, err := os.ReadFile(Path())
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", Path(), err)
	}
	if c.CurrencyRate == 0 {
		c.CurrencyRate = 1
	}
	if c.RefreshInterval.Duration <= 0 {
		c.RefreshInterval.Duration = 5 * time.Second
	}
	if len(c.Budgets.Thresholds) == 0 {
		c.Budgets.Thresholds = []float64{0.5, 0.8, 1.0}
	}
	if c.Agents == nil {
		c.Agents = map[string]AgentConfig{}
	}
	return c, nil
}

// Save writes the config atomically with user-only permissions.
func (c *Config) Save() error {
	p := Path()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append([]byte(header), data...), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// DBPath is the SQLite database location.
func (c *Config) DBPath() string {
	if c.DatabasePath != "" {
		return expand(c.DatabasePath)
	}
	return filepath.Join(DataDir(), "usage.db")
}

// Agent returns the config for a named adapter.
func (c *Config) Agent(name string) AgentConfig { return c.Agents[name] }

// Paths returns user-overridden paths for an agent with ~ expanded.
func (a AgentConfig) ExpandedPaths() []string {
	out := make([]string, 0, len(a.Paths))
	for _, p := range a.Paths {
		out = append(out, expand(p))
	}
	return out
}

func expand(p string) string {
	if len(p) > 1 && p[:2] == "~/" {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[2:])
	}
	return p
}

const header = `# TokenManager configuration
# Docs: run "tokenmanager config --help". All money values use the "currency" field.
# pricing: USD per million tokens, e.g.
#   pricing:
#     my-local-model: {input: 1.25, output: 10}
# budgets.per_agent / per_project / per_model take daily/weekly/monthly limits.
# Budgets are monitoring only; they never enforce provider-side limits.
# plans.anthropic / plans.openai describe subscription windows (see config.example.yaml).
`
