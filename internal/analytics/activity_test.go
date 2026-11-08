package analytics

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/ht2803-max/tokenmanager/internal/config"
	"github.com/ht2803-max/tokenmanager/internal/model"
	"github.com/ht2803-max/tokenmanager/internal/storage"
)

func activityEngine(t *testing.T, now time.Time, recs []model.Record) *Engine {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for i := range recs {
		recs[i].Key = fmt.Sprint("k", i)
	}
	if _, err := db.Insert(recs); err != nil {
		t.Fatal(err)
	}
	e := New(config.Default(), db)
	e.Now = func() time.Time { return now }
	return e
}

func rec(agent string, ts time.Time, tokens int64) model.Record {
	return model.Record{Agent: agent, Provider: "p", Model: "m", SessionID: "s", Timestamp: ts, Input: tokens / 2, Output: tokens - tokens/2, Accuracy: model.Actual}
}

func TestActivityDaysAndFilter(t *testing.T) {
	loc := time.FixedZone("IST", 5*3600+1800)
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, loc) // Saturday
	recs := []model.Record{
		rec("codex", time.Date(2026, 11, 7, 0, 10, 0, 0, loc), 100),       // today, just after local midnight
		rec("codex", time.Date(2026, 11, 6, 23, 50, 0, 0, loc), 50),       // yesterday, just before
		rec("claude-code", time.Date(2026, 11, 6, 9, 0, 0, 0, loc), 1000), // yesterday, other agent
		rec("codex", time.Date(2026, 11, 3, 9, 0, 0, 0, loc), 10),         // Tuesday
		rec("codex", time.Date(2025, 1, 1, 9, 0, 0, 0, loc), 99999),       // outside the window
	}
	e := activityEngine(t, now, recs)
	a, err := e.Activity(storage.Filter{}, 53)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Days) != 52*7+6 || a.Days[0].Date.Weekday() != time.Monday {
		t.Fatalf("days: %d, first %v", len(a.Days), a.Days[0].Date)
	}
	last := a.Days[len(a.Days)-1]
	if last.Date.Day() != 7 || last.Tokens != 100 || a.Days[len(a.Days)-2].Tokens != 1050 {
		t.Fatalf("last days: %+v %+v", a.Days[len(a.Days)-2], last)
	}
	if a.Total != 1160 || a.ActiveDays != 3 || a.Streak != 2 || a.LongestStreak != 2 || a.Peak.Tokens != 1050 {
		t.Fatalf("stats: %+v", a)
	}
	c, err := e.Activity(storage.Filter{Agent: "codex"}, 53)
	if err != nil {
		t.Fatal(err)
	}
	if c.Total != 160 {
		t.Fatalf("codex total %d", c.Total)
	}
}

func TestActivityStreakEndingYesterdayAndLevels(t *testing.T) {
	today := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	start := today.AddDate(0, 0, -9)
	day := func(n int) int64 { return start.AddDate(0, 0, n).UnixMilli() / storage.BucketMillis }
	// days 0-3 active, gap, 6-8 active (8 = yesterday), today idle.
	b := map[int64]int64{day(0): 10, day(1): 20, day(2): 30, day(3): 40, day(6): 50, day(7): 60, day(8): 70}
	a := buildActivity(b, start, today)
	if a.Streak != 3 || a.LongestStreak != 4 || a.ActiveDays != 7 {
		t.Fatalf("streaks: %+v", a)
	}
	if a.Level(0) != 0 || a.Level(10) != 1 || a.Level(70) != 4 || a.Level(a.Thresholds[1]) != 3 {
		t.Fatalf("levels: thresholds %v", a.Thresholds)
	}
}

func TestActivityAcrossDST(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tzdata")
	}
	today := time.Date(2026, 11, 2, 0, 0, 0, 0, loc) // DST ended Nov 1
	start := time.Date(2026, 10, 26, 0, 0, 0, 0, loc)
	at := func(t time.Time) int64 { return t.UnixMilli() / storage.BucketMillis }
	b := map[int64]int64{
		at(time.Date(2026, 11, 1, 23, 45, 0, 0, loc)): 5,
		at(time.Date(2026, 11, 2, 0, 0, 0, 0, loc)):   7,
	}
	a := buildActivity(b, start, today)
	if len(a.Days) != 8 || a.Days[6].Tokens != 5 || a.Days[7].Tokens != 7 {
		t.Fatalf("days: %+v", a.Days)
	}
}
