package pricing

import (
	"math"
	"testing"

	"github.com/tokenmanager/tokenmanager/internal/config"
	"github.com/tokenmanager/tokenmanager/internal/model"
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
	// Must not borrow gpt-5's price just because the name looks similar.
	if _, ok := tb.Cost(model.Record{Model: "gpt-5.6-terra", Input: 100}); ok {
		t.Fatal("unknown model should be unpriced")
	}
}

func TestConfigOverride(t *testing.T) {
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
