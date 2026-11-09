package status

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ht2803-max/tokenmanager/internal/config"
	"github.com/ht2803-max/tokenmanager/internal/model"
)

func ev(sid string, st model.State, at time.Time) model.StatusEvent {
	return model.StatusEvent{Agent: "claude-code", SessionID: sid, Project: "/p", State: st, At: at}
}

func TestTrackerTransitions(t *testing.T) {
	t0 := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	tr := NewTracker()
	if got := tr.Update([]model.StatusEvent{ev("a", model.StateWaiting, t0)}); len(got) != 0 {
		t.Fatalf("seed emitted %d transitions", len(got))
	}
	// waiting -> working -> waiting
	got := tr.Update([]model.StatusEvent{ev("a", model.StateWorking, t0.Add(time.Second))})
	if len(got) != 1 || got[0].From != model.StateWaiting || got[0].Event.State != model.StateWorking {
		t.Fatalf("to working: %+v", got)
	}
	got = tr.Update([]model.StatusEvent{ev("a", model.StateWaiting, t0.Add(time.Second))})
	if len(got) != 1 || got[0].Event.State != model.StateWaiting {
		t.Fatalf("to waiting (same At, time passed): %+v", got)
	}
	// duplicate: no transition
	if got := tr.Update([]model.StatusEvent{ev("a", model.StateWaiting, t0.Add(time.Second))}); len(got) != 0 {
		t.Fatalf("duplicate: %+v", got)
	}
	// out of order (older At) is ignored
	if got := tr.Update([]model.StatusEvent{ev("a", model.StateDone, t0)}); len(got) != 0 {
		t.Fatalf("older event applied: %+v", got)
	}
	if s := tr.Sessions(); len(s) != 1 || s[0].State != model.StateWaiting {
		t.Fatalf("state after old event: %+v", s)
	}
	// a new session appearing after seeding is a transition from ""
	got = tr.Update([]model.StatusEvent{ev("a", model.StateWaiting, t0.Add(time.Second)), ev("b", model.StateWorking, t0)})
	if len(got) != 1 || got[0].From != "" || got[0].Event.SessionID != "b" {
		t.Fatalf("new session: %+v", got)
	}
	// sessions missing from a snapshot are dropped silently
	if got := tr.Update(nil); len(got) != 0 || len(tr.Sessions()) != 0 {
		t.Fatalf("drop: %+v %+v", got, tr.Sessions())
	}
}

func setup(t *testing.T) (*Scanner, string, *time.Time) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	off := false
	for _, a := range []string{"codex", "gemini-cli", "aider", "continue", "cursor"} {
		cfg.Agents[a] = config.AgentConfig{Enabled: &off}
	}
	cfg.Agents["claude-code"] = config.AgentConfig{Paths: []string{dir}}
	s := NewScanner(cfg)
	now := time.Now()
	s.Now = func() time.Time { return now }
	return s, dir, &now
}

func write(t *testing.T, path string, mtime time.Time, ls ...string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(strings.Join(ls, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(path, mtime, mtime)
}

const (
	prompt  = `{"type":"user","sessionId":"S","cwd":"/p/api","message":{"content":"hi"}}`
	toolUse = `{"type":"assistant","sessionId":"S","cwd":"/p/api","message":{"stop_reason":"tool_use","content":[{"type":"tool_use","id":"t1","name":"Task"}]}}`
	endTurn = `{"type":"assistant","sessionId":"S","cwd":"/p/api","message":{"stop_reason":"end_turn","content":[{"type":"text","text":"ok"}]}}`
	side    = `{"type":"assistant","isSidechain":true,"sessionId":"S","cwd":"/p/api","message":{"content":[{"type":"text","text":"x"}]}}`
)

func TestScanMergesSubagentAndCaches(t *testing.T) {
	s, dir, now := setup(t)
	main := filepath.Join(dir, "p", "S.jsonl")
	sub := filepath.Join(dir, "p", "S", "subagents", "agent-1.jsonl")
	write(t, main, now.Add(-time.Minute), prompt, toolUse) // Task pending, main file silent
	write(t, sub, now.Add(-time.Second), side)             // subagent busy

	evs := s.Scan()
	if len(evs) != 1 || evs[0].State != model.StateWorking || evs[0].Detail != "Subagent running" {
		t.Fatalf("subagent should make the session working: %+v", evs)
	}
	// subagent finished long ago: the pending Task now reads as waiting
	write(t, sub, now.Add(-20*time.Minute), side)
	evs = s.Scan()
	if len(evs) != 1 || evs[0].State != model.StateWaiting {
		t.Fatalf("want waiting: %+v", evs)
	}
	// unchanged file, but time crosses idle: re-probed without new writes
	write(t, main, now.Add(-time.Minute), prompt, endTurn)
	if evs = s.Scan(); evs[0].State != model.StateDone {
		t.Fatalf("want done: %+v", evs)
	}
	*now = now.Add(10 * time.Minute)
	if evs = s.Scan(); evs[0].State != model.StateIdle {
		t.Fatalf("want idle after threshold: %+v", evs)
	}
}

func TestScanHidesStale(t *testing.T) {
	s, dir, now := setup(t)
	write(t, filepath.Join(dir, "p", "old.jsonl"), now.Add(-3*time.Hour), prompt, endTurn)
	if evs := s.Scan(); len(evs) != 0 {
		t.Fatalf("stale shown: %+v", evs)
	}
	s.IncludeStale = true
	if evs := s.Scan(); len(evs) != 1 || evs[0].State != model.StateStale {
		t.Fatalf("--stale: %+v", evs)
	}
}

func TestScanHideProjectPaths(t *testing.T) {
	s, dir, now := setup(t)
	s.cfg.Privacy.HideProjectPaths = true
	write(t, filepath.Join(dir, "p", "S.jsonl"), now.Add(-time.Second), prompt)
	if evs := s.Scan(); len(evs) != 1 || evs[0].Project != "api" {
		t.Fatalf("project path not hidden: %+v", evs)
	}
}
