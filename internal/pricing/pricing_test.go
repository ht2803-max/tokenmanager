package pricing

import (
	"math"
	"testing"

	"github.com/ht2803-max/tokenmanager/internal/config"
	"github.com/ht2803-max/tokenmanager/internal/model"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestCostOpus55(t *testing.T) {
	tb := New(nil)
	// Real request from a Claude Code transcript.
	r := model.Record{Model: "claude-opus-5-5", Input: 2, Output: 567, CacheRead: 42622, CacheWrite1h: 25816}
	c, ok := tb.Cost(r)
	want := (2*4 + 567*20 + 42622*0.20 + 25816*8.0) / 1e6
	if !ok || !near(c, want) {
		t.Fatalf("cost = %v, want %v", c, want)
	}
}

func TestCacheMultipliers(t *testing.T) {
	p, _ := New(nil).Lookup("claude-sonnet-4-6")
	if !near(p.CacheRead, 0.3) || !near(p.CacheWrite5m, 3.75) || !near(p.CacheWrite1h, 6) {
		t.Fatalf("got %+v", p)
	}
}

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"claude-sonnet-4-5-20250929":              "claude-sonnet-4-5",
		"anthropic.claude-opus-4-1-20250805-v1:0": "claude-opus-4-1",
		"us.anthropic.claude-haiku-4-5":           "claude-haiku-4-5",
		"claude-opus-4-5@20251101":                "claude-opus-4-5",
		"claude-opus-5-5[1m]":                     "claude-opus-5-5",
		"openrouter/anthropic/claude-sonnet-4":    "claude-sonnet-4",
		"GPT-5":                                   "gpt-5",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUnknownModelIsUnpriced(t *testing.T) {
	tb := New(nil)
	// Must not borrow gpt-5.6-terra's price just because the name looks similar.
	if _, ok := tb.Cost(model.Record{Model: "gpt-5.7-terra", Input: 100}); ok {
		t.Fatal("unknown model should be unpriced")
	}
}

func TestConfigOverride(t *testing.T) {
	// A config price replaces the built-in one.
	cfg := config.Default()
	cfg.Pricing = map[string]config.ModelPrice{"gpt-5.6-terra": {Input: 1, Output: 10}}
	cfg.CurrencyRate = 2
	c, ok := New(cfg).Cost(model.Record{Model: "gpt-5.6-terra", Input: 1e6, CacheRead: 1e6})
	if !ok || !near(c, (1+0.1)*2) {
		t.Fatalf("cost = %v ok=%v", c, ok)
	}
}

func TestLongContextAndFast(t *testing.T) {
	tb := New(nil)
	short, _ := tb.Cost(model.Record{Model: "claude-haiku-5-5", Input: 100_000, Output: 1000})
	long, _ := tb.Cost(model.Record{Model: "claude-haiku-5-5", Input: 100_001, Output: 1000})
	if !near(short, (100_000*0.1+1000*0.5)/1e6) || !near(long, (100_001*0.5+1000*2.5)/1e6) {
		t.Fatalf("short=%v long=%v", short, long)
	}
	std, _ := tb.Cost(model.Record{Model: "claude-opus-5-5", Output: 1000})
	fast, _ := tb.Cost(model.Record{Model: "claude-opus-5-5", Output: 1000, Speed: "fast"})
	if !near(fast, std*2) {
		t.Fatalf("fast=%v std=%v", fast, std)
	}
}

func TestReportedCostWins(t *testing.T) {
	c, ok := New(nil).Cost(model.Record{Model: "something-unknown", Input: 5, ReportedCost: 0.42})
	if !ok || !near(c, 0.42) {
		t.Fatalf("cost = %v", c)
	}
}

func TestOpenAIPrices(t *testing.T) {
	// OpenAI's table as provided 2026-11-01: input, output, cached input,
	// 30m write, 12h write. 0 marks a "-" cell.
	table := map[string][5]float64{
		"gpt-6-astra":            {10, 50, 1, 12.5, 25},
		"gpt-6.1-sol":            {2, 10, 0.10, 2.5, 5},
		"gpt-6.1-terra":          {1, 5, 0.05, 1.25, 2.5},
		"gpt-6-luna":             {0.10, 0.50, 0.01, 0.125, 0.25},
		"gpt-6-sol":              {2, 10, 0.20, 2.5, 5},
		"gpt-5.6-sol":            {4, 20, 0.40, 5, 10},
		"gpt-5.6-terra":          {2, 12, 0.20, 2.5, 5},
		"gpt-5.6-luna":           {0.20, 1.20, 0.02, 0.25, 0.50},
		"gpt-5.5":                {5, 30, 0.50, 0, 0},
		"gpt-5.5-pro":            {30, 180, 0, 0, 0},
		"gpt-5.4":                {2.5, 15, 0.25, 0, 0},
		"gpt-5.4-pro":            {30, 180, 0, 0, 0},
		"gpt-5.4-mini":           {0.75, 4.50, 0.075, 0, 0},
		"gpt-5.4-nano":           {0.20, 1.25, 0.02, 0, 0},
		"gpt-5.2":                {1.75, 14, 0.175, 0, 0},
		"gpt-5.2-pro":            {21, 168, 0, 0, 0},
		"gpt-5.1":                {1.25, 10, 0.125, 0, 0},
		"gpt-5":                  {1.25, 10, 0.125, 0, 0},
		"gpt-5-mini":             {0.25, 2, 0.025, 0, 0},
		"gpt-5-nano":             {0.05, 0.40, 0.005, 0, 0},
		"gpt-5-pro":              {15, 120, 0, 0, 0},
		"gpt-4.1":                {2, 8, 0.50, 0, 0},
		"gpt-4.1-mini":           {0.40, 1.60, 0.10, 0, 0},
		"gpt-4.1-nano":           {0.10, 0.40, 0.025, 0, 0},
		"gpt-4o":                 {2.5, 10, 1.25, 0, 0},
		"gpt-4o-mini":            {0.15, 0.60, 0.075, 0, 0},
		"o4-mini":                {1.10, 4.40, 0.275, 0, 0},
		"o3":                     {2, 8, 0.50, 0, 0},
		"o3-mini":                {1.10, 4.40, 0.55, 0, 0},
		"o3-pro":                 {20, 80, 0, 0, 0},
		"o1":                     {15, 60, 7.50, 0, 0},
		"o1-pro":                 {150, 600, 0, 0, 0},
		"gpt-4o-2024-05-13":      {5, 15, 0, 0, 0},
		"gpt-4-turbo-2024-04-09": {10, 30, 0, 0, 0},
		"gpt-4-0613":             {30, 60, 0, 0, 0},
		"gpt-3.5-turbo":          {0.50, 1.50, 0, 0, 0},
		"gpt-3.5-turbo-0125":     {0.50, 1.50, 0, 0, 0},
	}
	tb := New(nil)
	for m, v := range table {
		p, ok := tb.Lookup(m)
		if !ok {
			t.Errorf("%s: not priced", m)
			continue
		}
		// "-" cells: no cached discount, and writes bill as plain input.
		want := [5]float64{v[0], v[1], v[2], v[3], v[4]}
		for i := 2; i < 5; i++ {
			if want[i] == 0 {
				want[i] = v[0]
			}
		}
		got := [5]float64{p.Input, p.Output, p.CacheRead, p.CacheWrite5m, p.CacheWrite1h}
		for i := range got {
			if !near(got[i], want[i]) {
				t.Errorf("%s: got %v, want %v", m, got, want)
				break
			}
		}
	}
	// Dated snapshots with their own price are not folded into the base model.
	if p, _ := tb.Lookup("gpt-4o-2024-05-13"); p.Input != 5 {
		t.Fatalf("gpt-4o-2024-05-13 resolved to %+v", p)
	}
}

func TestCostCodexCacheWrite(t *testing.T) {
	// Codex records cache writes in the default slot: the 30 minute rate.
	r := model.Record{Model: "gpt-6-sol", Input: 1000, Output: 500, CacheRead: 20000, CacheWrite5m: 4000}
	c, ok := New(nil).Cost(r)
	want := (1000*2 + 500*10 + 20000*0.20 + 4000*2.5) / 1e6
	if !ok || !near(c, want) {
		t.Fatalf("cost = %v, want %v", c, want)
	}
}
