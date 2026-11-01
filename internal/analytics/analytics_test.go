package analytics

import (
	"testing"
	"time"

	"github.com/ht2803-max/tokenmanager/internal/config"
	"github.com/ht2803-max/tokenmanager/internal/model"
	"github.com/ht2803-max/tokenmanager/internal/pricing"
)

func TestPeriods(t *testing.T) {
	now := time.Date(2026, 10, 31, 15, 0, 0, 0, time.Local) // Saturday
	if p := PeriodFor("week", now); p.From.Weekday() != time.Monday || p.From.Day() != 26 {
		t.Fatalf("week starts %v", p.From)
	}
	if p := PeriodFor("month", now); p.From.Day() != 1 || !p.To.Equal(time.Date(2026, 11, 1, 0, 0, 0, 0, time.Local)) {
		t.Fatalf("month %v-%v", p.From, p.To)
	}
	if p := PeriodFor("30d", now); p.From.Day() != 2 {
		t.Fatalf("30d from %v", p.From)
	}
}

func TestProjectMonth(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC) // 15.5 of 31 days elapsed
	if got := ProjectMonth(50, now); got < 99.9 || got > 100.1 {
		t.Fatalf("projection = %v, want ~100", got)
	}
}

func TestGroupAndSessions(t *testing.T) {
	e := &Engine{Prices: pricing.New(config.Default()), Cfg: config.Default(), Now: time.Now}
	t0 := time.Date(2026, 10, 30, 10, 0, 0, 0, time.Local)
	recs := []model.Record{
		{Agent: "claude-code", Model: "claude-opus-5-5", SessionID: "a", Project: "/p1", Timestamp: t0, Input: 10, Output: 1000, CacheRead: 5000},
		{Agent: "claude-code", Model: "claude-opus-5-5", SessionID: "a", Project: "/p1", Timestamp: t0.Add(time.Hour), Input: 10, Output: 1000, CacheRead: 9000},
		{Agent: "codex", Model: "gpt-unknown", SessionID: "b", Project: "/p2", Timestamp: t0.Add(2 * time.Hour), Input: 100, Output: 10},
	}
	gs := e.Group(recs, ByAgent)
	if len(gs) != 2 || gs[0].Key != "claude-code" || gs[0].Requests != 2 || gs[1].Unpriced != 110 {
		t.Fatalf("groups: %+v %+v", gs[0], gs[1])
	}
	ss := e.Sessions(recs)
	if len(ss) != 2 || ss[0].ID != "b" || ss[1].Duration() != time.Hour || ss[1].LastPrompt != 9010 || ss[1].ContextWindow != 1_000_000 {
		t.Fatalf("sessions: %+v", ss[1])
	}
	pts := e.DailySeries(recs, 3, t0.Add(3*time.Hour))
	if len(pts) != 3 || pts[2].Tokens != recs[0].Total()+recs[1].Total()+recs[2].Total() {
		t.Fatalf("series: %+v", pts)
	}
}
