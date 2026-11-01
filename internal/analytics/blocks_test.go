package analytics

import (
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/ht2803-max/tokenmanager/internal/config"
	"github.com/ht2803-max/tokenmanager/internal/model"
	"github.com/ht2803-max/tokenmanager/internal/pricing"
	"github.com/ht2803-max/tokenmanager/internal/storage"
)

var t0 = time.Date(2026, 10, 28, 10, 17, 0, 0, time.UTC)

func blockEngine(cfg *config.Config) *Engine {
	return &Engine{Prices: pricing.New(cfg), Cfg: cfg, Now: time.Now}
}

// req is a Claude Code request with in+out counted tokens.
func req(ts time.Time, in, out int64) model.Record {
	return model.Record{Agent: "claude-code", Provider: "anthropic", Model: "claude-opus-5-5", SessionID: "s", Timestamp: ts, Input: in, Output: out, CacheRead: 1000}
}

func TestGroupBlocks(t *testing.T) {
	e := blockEngine(config.Default())
	opt := BlockOptions{Length: 5 * time.Hour, Floor: true, Count: []string{"input", "output"}}
	recs := []model.Record{
		req(t0, 10, 90),                                  // starts 10:00 (floored)
		req(t0.Add(time.Hour), 10, 90),                   // same block
		req(t0.Add(-17*time.Minute+5*time.Hour), 10, 90), // exactly 15:00: the boundary starts a new block
		req(t0.Add(11*time.Hour), 10, 90),                // 21:17, gap > 5h: block from 21:00
	}
	now := t0.Add(12 * time.Hour)
	bs := e.GroupBlocks(recs, "anthropic", opt, now)
	if len(bs) != 3 {
		t.Fatalf("got %d blocks, want 3", len(bs))
	}
	wantStarts := []int{10, 15, 21}
	for i, b := range bs {
		if b.Start.Hour() != wantStarts[i] || b.Start.Minute() != 0 || b.End.Sub(b.Start) != 5*time.Hour {
			t.Errorf("block %d: %v → %v", i, b.Start, b.End)
		}
	}
	if bs[0].Requests != 2 || bs[0].Counted != 200 || bs[0].CacheRead != 2000 {
		t.Errorf("block 0 totals: %+v", bs[0])
	}
	if bs[0].Active || bs[1].Active || !bs[2].Active {
		t.Errorf("active flags: %v %v %v", bs[0].Active, bs[1].Active, bs[2].Active)
	}

	// Without flooring the block starts at the first request itself.
	opt.Floor = false
	if bs := e.GroupBlocks(recs[:1], "anthropic", opt, now); !bs[0].Start.Equal(t0) {
		t.Errorf("unfloored start %v", bs[0].Start)
	}
	// Configurable length: 1h blocks split the first two requests.
	opt.Length = time.Hour
	if bs := e.GroupBlocks(recs[:2], "anthropic", opt, now); len(bs) != 2 {
		t.Errorf("1h blocks: got %d", len(bs))
	}
	if bs := e.GroupBlocks(nil, "anthropic", opt, now); len(bs) != 0 {
		t.Errorf("empty dataset: %v", bs)
	}
}

func TestBurnRate(t *testing.T) {
	cfg := config.Default()
	e := blockEngine(cfg)
	start := t0.Truncate(time.Hour)

	// Steady: 100 counted tokens a minute.
	var steady []model.Record
	for i := 1; i <= 30; i++ {
		steady = append(steady, req(start.Add(time.Duration(i)*time.Minute), 0, 100))
	}
	now := start.Add(30*time.Minute + 30*time.Second)
	r := e.PlanReport("anthropic", steady, nil, now)
	s := r.Session
	if s == nil || s.Burn.Basis != "last 15m" || math.Abs(s.Burn.TokensPerMin-100) > 0.01 {
		t.Fatalf("steady burn: %+v", s.Burn)
	}
	left := 5*time.Hour - 30*time.Minute - 30*time.Second
	if want := int64(3000 + 100*left.Minutes()); s.Projected != want {
		t.Errorf("projected %d, want %d", s.Projected, want)
	}

	// Burst: a slow first hour, then 30k tokens in five minutes.
	var burst []model.Record
	for i := 1; i <= 60; i++ {
		burst = append(burst, req(start.Add(time.Duration(i)*time.Minute), 0, 10))
	}
	for i := 0; i < 3; i++ {
		burst = append(burst, req(start.Add(time.Duration(61+i)*time.Minute), 0, 10_000))
	}
	now = start.Add(65 * time.Minute)
	s = e.PlanReport("anthropic", burst, nil, now).Session
	avg := float64(s.Counted) / 65
	if s.Burn.Basis != "last 15m" || s.Burn.TokensPerMin < 2000 || s.Burn.TokensPerMin < 3*avg {
		t.Fatalf("burst burn %+v (block average %.0f)", s.Burn, avg)
	}

	// One request: falls back to the block average since the floored start.
	one := []model.Record{req(start.Add(30*time.Minute), 0, 600)}
	now = start.Add(time.Hour)
	s = e.PlanReport("anthropic", one, nil, now).Session
	if s.Burn.Basis != "window average" || math.Abs(s.Burn.TokensPerMin-10) > 0.01 || s.Projected != 600+10*240 {
		t.Fatalf("single request: %+v projected %d", s.Burn, s.Projected)
	}
	if s.Limit.Source != SourceNone || s.ETAKnown || s.Outlook(now) != "no limit known" {
		t.Fatalf("no history and no config must not invent a limit: %+v", s.Limit)
	}
}

func TestLimitPrecedence(t *testing.T) {
	start := t0.Truncate(time.Hour)
	recs := []model.Record{
		req(start.Add(-24*time.Hour), 0, 1000), // a completed block yesterday
		req(start.Add(time.Minute), 0, 125),
		req(start.Add(2*time.Minute), 0, 125),
	}
	now := start.Add(10 * time.Minute)

	cfg := config.Default()
	e := blockEngine(cfg)
	if l := e.PlanReport("anthropic", recs[1:], nil, now).Session.Limit; l.Source != SourceNone {
		t.Errorf("none: %+v", l)
	}
	s := e.PlanReport("anthropic", recs, nil, now).Session
	if l := s.Limit; l.Source != SourcePersonalMax || l.Tokens != 1000 || l.Fraction != 0.25 || !l.Source.Estimated() {
		t.Errorf("personal max: %+v", l)
	}
	cfg.Plans.Anthropic.SessionTokens = 2000
	s = e.PlanReport("anthropic", recs, nil, now).Session
	if l := s.Limit; l.Source != SourceConfigured || l.Tokens != 2000 || l.Fraction != 0.125 {
		t.Errorf("configured: %+v", l)
	}
	// ETA = (2000 - 250) / burn.
	if want := time.Duration(1750 / s.Burn.TokensPerMin * float64(time.Minute)); !s.ETAKnown || s.ETA != want {
		t.Errorf("eta %v, want %v", s.ETA, want)
	}

	// Reported beats configured, and its window replaces the estimated block.
	codex := make([]model.Record, len(recs))
	for i, r := range recs {
		r.Agent, r.Provider = "codex", "openai"
		codex[i] = r
	}
	cfg.Plans.OpenAI.SessionTokens = 2000
	snap := &model.RateLimits{Provider: "openai", Agent: "codex", Plan: "plus", Observed: now,
		Primary: &model.RateLimitWindow{UsedPercent: 50, WindowMinutes: 300, ResetsAt: start.Add(4 * time.Hour)}}
	rep := e.PlanReport("openai", codex, snap, now)
	s = rep.Session
	if l := s.Limit; l.Source != SourceReported || l.Fraction != 0.5 || !l.Implied || l.Tokens != 500 {
		t.Errorf("reported: %+v", l)
	}
	if !s.Reported || !s.Start.Equal(start.Add(-time.Hour)) || !s.End.Equal(start.Add(4*time.Hour)) || rep.Plan != "plus" {
		t.Errorf("reported window %v → %v (reported=%v)", s.Start, s.End, s.Reported)
	}
	if n := len(rep.History); n != 2 || !rep.History[0].Reported {
		t.Errorf("history should hold the reported block and yesterday's: %d", n)
	}
	// An expired snapshot no longer counts as reported.
	snap.Primary.ResetsAt = now.Add(-time.Minute)
	if l := e.PlanReport("openai", codex, snap, now).Session.Limit; l.Source != SourceConfigured {
		t.Errorf("expired snapshot: %+v", l)
	}
}

func TestWeeklyWindow(t *testing.T) {
	now := time.Date(2026, 11, 1, 13, 0, 0, 0, time.UTC) // Sunday
	for spec, want := range map[string]time.Time{
		"mon 09:00":    time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC),
		"Sunday 14:00": time.Date(2026, 10, 25, 14, 0, 0, 0, time.UTC),
		"sun 12:00":    time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC),
	} {
		if got, ok := lastWeeklyReset(spec, now); !ok || !got.Equal(want) {
			t.Errorf("%q: %v, want %v", spec, got, want)
		}
	}
	for _, bad := range []string{"", "funday 09:00", "mon 25:00", "mon"} {
		if _, ok := lastWeeklyReset(bad, now); ok {
			t.Errorf("%q should not parse", bad)
		}
	}

	cfg := config.Default()
	cfg.Plans.Anthropic.WeeklyReset = "mon 09:00"
	cfg.Plans.Anthropic.WeeklyTokens = 10_000
	e := blockEngine(cfg)
	recs := []model.Record{req(now.Add(-8*24*time.Hour), 0, 7000), req(now.Add(-2*time.Hour), 0, 1000), req(now.Add(-time.Hour), 0, 1000)}
	wk := e.PlanReport("anthropic", recs, nil, now).Weekly
	if wk.Rolling || wk.Counted != 2000 || wk.Limit.Source != SourceConfigured || wk.Limit.Fraction != 0.2 || !wk.End.Equal(time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("fixed weekly: %+v", wk)
	}
	cfg.Plans.Anthropic.WeeklyReset, cfg.Plans.Anthropic.WeeklyTokens = "", 0
	wk = e.PlanReport("anthropic", recs, nil, now).Weekly
	if !wk.Rolling || wk.Counted != 2000 || wk.Limit.Source != SourcePersonalMax || wk.Limit.Tokens != 7000 || wk.Remaining(now) != 0 {
		t.Errorf("rolling weekly: %+v", wk)
	}
}

func TestLimitsSeparatesProviders(t *testing.T) {
	cfg := config.Default()
	db, err := storage.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now()
	e := New(cfg, db)
	e.Now = func() time.Time { return now }

	if reps, err := e.Limits(storage.Filter{}); err != nil || len(reps) != 0 {
		t.Fatalf("empty database: %v %v", reps, err)
	}
	mk := func(key, agent, provider string, out int64) model.Record {
		return model.Record{Key: key, Agent: agent, Provider: provider, Model: "m", SessionID: "s", Timestamp: now.Add(-time.Minute), Output: out}
	}
	if _, err := db.Insert([]model.Record{mk("a", "claude-code", "anthropic", 100), mk("b", "codex", "openai", 200), mk("c", "aider", "anthropic", 400)}); err != nil {
		t.Fatal(err)
	}
	reps, err := e.Limits(storage.Filter{})
	if err != nil || len(reps) != 2 {
		t.Fatalf("got %d reports: %v", len(reps), err)
	}
	if reps[0].Family != "anthropic" || reps[0].Session.Counted != 100 || reps[1].Family != "openai" || reps[1].Session.Counted != 200 {
		t.Fatalf("families mixed or aider counted: %+v / %+v", reps[0].Session, reps[1].Session)
	}
	// An explicit --agent bypasses the plan's agent list.
	reps, _ = e.Limits(storage.Filter{Agent: "aider"})
	if len(reps) != 1 || reps[0].Session.Counted != 400 {
		t.Fatalf("--agent aider: %+v", reps)
	}
}
