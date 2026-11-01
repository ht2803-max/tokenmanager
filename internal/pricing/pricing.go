// Package pricing estimates request cost from list prices.
//
// Costs are estimates at public API list prices. Subscription plans
// (Claude Pro/Max, ChatGPT Plus/Pro) are not billed per token, so for those
// users the numbers are the API-equivalent value of the usage.
package pricing

import (
	"regexp"
	"strings"

	"github.com/tokenmanager/tokenmanager/internal/config"
	"github.com/tokenmanager/tokenmanager/internal/model"
)

// Price is USD per million tokens.
type Price struct {
	Input, Output, CacheRead, CacheWrite5m, CacheWrite1h float64
	// FastMultiplier applies when a request used a premium speed tier.
	FastMultiplier float64
	// LongContext prices apply when the prompt exceeds LongThreshold tokens.
	LongThreshold int64
	LongInput     float64
	LongOutput    float64
	ContextWindow int64
}

func std(in, out float64) Price {
	return Price{Input: in, Output: out, CacheRead: in * 0.1, CacheWrite5m: in * 1.25, CacheWrite1h: in * 2}
}

func withRead(p Price, read float64) Price { p.CacheRead = read; return p }

// defaults are list prices. Anthropic prices were taken from the Claude API
// reference (cached 2026-10-06). OpenAI and Google entries cover only models
// whose published prices are well established; anything else is unpriced
// until the user adds it to config.yaml. Prices are never guessed from a
// similar-looking model name.
var defaults = map[string]Price{
	// Anthropic
	"claude-fable-5-1":  withRead(ctx(std(10, 50), 1_000_000), 0.25),
	"claude-mythos-5-1": withRead(ctx(std(10, 50), 1_000_000), 0.25),
	"claude-fable-5":    withRead(ctx(std(10, 50), 1_000_000), 1.00),
	"claude-mythos-5":   withRead(ctx(std(10, 50), 1_000_000), 1.00),
	"claude-opus-5-5":   fast(withRead(ctx(std(4, 20), 1_000_000), 0.20), 2),
	"claude-opus-5":     fast(ctx(std(5, 25), 1_000_000), 2),
	"claude-opus-4-8":   fast(ctx(std(5, 25), 1_000_000), 2),
	"claude-opus-4-7":   ctx(std(5, 25), 1_000_000),
	"claude-opus-4-6":   ctx(std(5, 25), 1_000_000),
	"claude-opus-4-5":   ctx(std(5, 25), 200_000),
	"claude-opus-4-1":   ctx(std(15, 75), 200_000),
	"claude-opus-4":     ctx(std(15, 75), 200_000),
	"claude-sonnet-5-5": withRead(ctx(std(2, 10), 1_000_000), 0.20),
	"claude-sonnet-5":   ctx(std(2, 10), 1_000_000),
	"claude-sonnet-4-6": ctx(std(3, 15), 1_000_000),
	"claude-sonnet-4-5": ctx(std(3, 15), 200_000),
	"claude-sonnet-4":   ctx(std(3, 15), 200_000),
	"claude-haiku-5-5":  long(ctx(std(0.10, 0.50), 1_000_000), 100_000, 0.50, 2.50),
	"claude-haiku-4-5":  ctx(std(1, 5), 200_000),
	"claude-3-5-haiku":  ctx(std(0.8, 4), 200_000),

	// OpenAI (cached input is billed at 10% for the GPT-5 family)
	"gpt-5":       ctx(std(1.25, 10), 400_000),
	"gpt-5-mini":  ctx(std(0.25, 2), 400_000),
	"gpt-5-nano":  ctx(std(0.05, 0.40), 400_000),
	"gpt-5-codex": ctx(std(1.25, 10), 400_000),
	"gpt-4.1":     withRead(ctx(std(2, 8), 1_000_000), 0.5),
	"gpt-4o":      withRead(ctx(std(2.5, 10), 128_000), 1.25),
	"o3":          withRead(ctx(std(2, 8), 200_000), 0.5),
	"o4-mini":     withRead(ctx(std(1.1, 4.4), 200_000), 0.275),

	// Google (prices for prompts up to 200k tokens)
	"gemini-2.5-pro":   long(withRead(ctx(std(1.25, 10), 1_000_000), 0.31), 200_000, 2.5, 15),
	"gemini-2.5-flash": withRead(ctx(std(0.30, 2.5), 1_000_000), 0.075),
}

func ctx(p Price, w int64) Price    { p.ContextWindow = w; return p }
func fast(p Price, m float64) Price { p.FastMultiplier = m; return p }
func long(p Price, t int64, in, out float64) Price {
	p.LongThreshold, p.LongInput, p.LongOutput = t, in, out
	return p
}

// Table resolves model ids to prices.
type Table struct {
	prices map[string]Price
	rate   float64
}

// New builds a table from defaults plus user overrides.
func New(cfg *config.Config) *Table {
	t := &Table{prices: make(map[string]Price, len(defaults)), rate: 1}
	for k, v := range defaults {
		t.prices[k] = v
	}
	if cfg != nil {
		if cfg.CurrencyRate > 0 {
			t.rate = cfg.CurrencyRate
		}
		for k, v := range cfg.Pricing {
			p := std(v.Input, v.Output)
			if v.CacheRead > 0 {
				p.CacheRead = v.CacheRead
			}
			if v.CacheWrite5m > 0 {
				p.CacheWrite5m = v.CacheWrite5m
			}
			if v.CacheWrite1h > 0 {
				p.CacheWrite1h = v.CacheWrite1h
			}
			if old, ok := t.prices[Normalize(k)]; ok {
				p.ContextWindow = old.ContextWindow
			}
			t.prices[Normalize(k)] = p
		}
	}
	return t
}

var (
	dateSuffix = regexp.MustCompile(`-\d{8}$`)
	atSuffix   = regexp.MustCompile(`@\d{8}$`)
)

// Normalize strips provider prefixes, date snapshots and context markers so
// "anthropic.claude-sonnet-4-5-20250929" resolves to "claude-sonnet-4-5".
func Normalize(m string) string {
	m = strings.ToLower(strings.TrimSpace(m))
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	for _, p := range []string{"anthropic.", "us.anthropic.", "eu.anthropic.", "global.anthropic."} {
		m = strings.TrimPrefix(m, p)
	}
	m = strings.TrimSuffix(m, "[1m]")
	m = strings.TrimSuffix(m, "-v1:0")
	m = atSuffix.ReplaceAllString(m, "")
	m = dateSuffix.ReplaceAllString(m, "")
	m = strings.TrimSuffix(m, "-latest")
	return m
}

// Lookup returns the price for a model and whether it is known.
func (t *Table) Lookup(modelID string) (Price, bool) {
	p, ok := t.prices[Normalize(modelID)]
	return p, ok
}

// Cost returns the estimated cost of a record in the configured currency and
// whether the model is priced. Agent-reported costs win when present.
func (t *Table) Cost(r model.Record) (float64, bool) {
	if r.ReportedCost > 0 {
		return r.ReportedCost * t.rate, true
	}
	p, ok := t.Lookup(r.Model)
	if !ok {
		return 0, false
	}
	in, out := p.Input, p.Output
	if p.LongThreshold > 0 && r.PromptTokens() > p.LongThreshold {
		scale := p.LongInput / p.Input
		in, out = p.LongInput, p.LongOutput
		p.CacheRead *= scale
		p.CacheWrite5m *= scale
		p.CacheWrite1h *= scale
	}
	c := (float64(r.Input)*in +
		float64(r.Output)*out +
		float64(r.CacheRead)*p.CacheRead +
		float64(r.CacheWrite5m)*p.CacheWrite5m +
		float64(r.CacheWrite1h)*p.CacheWrite1h) / 1e6
	if r.Speed == "fast" && p.FastMultiplier > 0 {
		c *= p.FastMultiplier
	}
	return c * t.rate, true
}

// ContextWindow returns the model's context window if known.
func (t *Table) ContextWindow(modelID string) int64 {
	p, _ := t.Lookup(modelID)
	return p.ContextWindow
}

// Models returns all priced model ids.
func (t *Table) Models() map[string]Price { return t.prices }
