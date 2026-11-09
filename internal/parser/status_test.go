package parser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ht2803-max/tokenmanager/internal/model"
)

// Hand-written fixtures: shapes match real transcripts, text is invented.
const (
	cPrompt  = `{"type":"user","sessionId":"s1","cwd":"/p/api","message":{"role":"user","content":"fix the bug"}}`
	cThink   = `{"type":"assistant","sessionId":"s1","cwd":"/p/api","message":{"stop_reason":"tool_use","content":[{"type":"thinking","thinking":"x"}]}}`
	cToolUse = `{"type":"assistant","sessionId":"s1","cwd":"/p/api","message":{"stop_reason":"tool_use","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}]}}`
	cAsk     = `{"type":"assistant","sessionId":"s1","cwd":"/p/api","message":{"stop_reason":"tool_use","content":[{"type":"tool_use","id":"t2","name":"AskUserQuestion","input":{}}]}}`
	cResult  = `{"type":"user","sessionId":"s1","cwd":"/p/api","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}`
	cEnd     = `{"type":"assistant","sessionId":"s1","cwd":"/p/api","message":{"stop_reason":"end_turn","content":[{"type":"text","text":"done"}]}}`
	cIntr    = `{"type":"user","sessionId":"s1","cwd":"/p/api","message":{"role":"user","content":[{"type":"text","text":"[Request interrupted by user for tool use]"}]}}`
	cSide    = `{"type":"assistant","isSidechain":true,"sessionId":"s1","cwd":"/p/api","message":{"stop_reason":"end_turn","content":[{"type":"text","text":"sub"}]}}`
	cNoise   = `{"type":"api-request","sessionId":"s1"}`
)

var popts = model.ProbeOptions{WaitingAfter: 8 * time.Second, IdleAfter: 10 * time.Minute, StaleAfter: 2 * time.Hour}

func lines(ls ...string) []byte { return []byte(strings.Join(ls, "\n") + "\n") }

func TestProbeClaude(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		tail   []byte
		age    time.Duration
		state  model.State
		detail string
		conf   model.Confidence
	}{
		{"prompt submitted", lines(cEnd, cPrompt), time.Second, model.StateWorking, "", model.High},
		{"thinking before tool", lines(cPrompt, cThink), time.Second, model.StateWorking, "", model.High},
		{"tool just requested", lines(cPrompt, cToolUse), 2 * time.Second, model.StateWorking, "Running Bash", model.High},
		{"tool unanswered", lines(cPrompt, cToolUse, cNoise), 30 * time.Second, model.StateWaiting, "Permission needed: Bash", model.Low},
		{"question", lines(cPrompt, cAsk), time.Minute, model.StateWaiting, "Question", model.Low},
		{"tool answered", lines(cPrompt, cToolUse, cResult), 30 * time.Second, model.StateWorking, "", model.High},
		{"turn ended", lines(cPrompt, cToolUse, cResult, cEnd), 30 * time.Second, model.StateDone, "", model.High},
		{"interrupted", lines(cPrompt, cToolUse, cIntr), time.Minute, model.StateDone, "", model.High},
		{"done goes idle", lines(cPrompt, cEnd), 11 * time.Minute, model.StateIdle, "", model.High},
		{"waiting stays waiting past idle", lines(cPrompt, cToolUse), 30 * time.Minute, model.StateWaiting, "Permission needed: Bash", model.Low},
		{"stale", lines(cPrompt, cToolUse), 3 * time.Hour, model.StateStale, "", model.High},
		{"sidechain ignored", lines(cPrompt, cToolUse, cResult, cEnd, cSide), time.Second, model.StateDone, "", model.High},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ev, ok := ProbeClaude("x.jsonl", c.tail, now.Add(-c.age), now, popts)
			if !ok {
				t.Fatal("probe returned no event")
			}
			if ev.State != c.state || ev.Detail != c.detail || ev.Confidence != c.conf {
				t.Fatalf("got %s/%q/%s, want %s/%q/%s", ev.State, ev.Detail, ev.Confidence, c.state, c.detail, c.conf)
			}
			if ev.SessionID != "s1" || ev.Project != "/p/api" || ev.Agent != "claude-code" {
				t.Fatalf("identity: %+v", ev)
			}
		})
	}
}

func TestProbeClaudeSubagentFile(t *testing.T) {
	now := time.Now()
	ev, ok := ProbeClaude("agent-x.jsonl", lines(cSide), now.Add(-time.Second), now, popts)
	if !ok || ev.State != model.StateWorking || ev.Detail != "Subagent running" || ev.SessionID != "s1" {
		t.Fatalf("active subagent: ok=%v %+v", ok, ev)
	}
	if _, ok := ProbeClaude("agent-x.jsonl", lines(cSide), now.Add(-time.Hour), now, popts); ok {
		t.Fatal("a finished subagent file should say nothing")
	}
	if _, ok := ProbeClaude("x.jsonl", lines(cNoise), now, now, popts); ok {
		t.Fatal("noise-only tail should say nothing")
	}
}

func TestProbeClaudeNoContentInEvent(t *testing.T) {
	now := time.Now()
	ev, _ := ProbeClaude("x.jsonl", lines(cPrompt, cToolUse), now.Add(-time.Minute), now, popts)
	for _, s := range []string{ev.Detail, ev.Project, ev.SessionID} {
		if strings.Contains(s, "fix the bug") || strings.Contains(s, "ls") {
			t.Fatalf("message content leaked into event: %+v", ev)
		}
	}
}

const (
	xMeta   = `{"timestamp":"2026-11-09T12:00:00Z","type":"session_meta","payload":{"id":"cx-1","cwd":"/p/web","base_instructions":"..."}}`
	xStart  = `{"timestamp":"2026-11-09T12:00:01Z","type":"event_msg","payload":{"type":"task_started"}}`
	xCall   = `{"timestamp":"2026-11-09T12:00:02Z","type":"response_item","payload":{"type":"function_call","name":"shell","call_id":"c1","arguments":"{}"}}`
	xOut    = `{"timestamp":"2026-11-09T12:00:03Z","type":"response_item","payload":{"type":"function_call_output","call_id":"c1","output":"ok"}}`
	xCustom = `{"timestamp":"2026-11-09T12:00:02Z","type":"response_item","payload":{"type":"custom_tool_call","name":"apply_patch","call_id":"c2","input":"..."}}`
	xDone   = `{"timestamp":"2026-11-09T12:00:04Z","type":"event_msg","payload":{"type":"task_complete"}}`
	xTokens = `{"timestamp":"2026-11-09T12:00:04Z","type":"event_msg","payload":{"type":"token_count"}}`
)

func TestProbeCodex(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 5, 0, 0, time.UTC)
	cases := []struct {
		name   string
		tail   []byte
		age    time.Duration
		state  model.State
		detail string
	}{
		{"turn started", lines(xMeta, xStart), time.Second, model.StateWorking, ""},
		{"call pending, recent", lines(xMeta, xStart, xCall), time.Second, model.StateWorking, "Running shell"},
		{"call pending, silent", lines(xMeta, xStart, xCustom, xTokens), time.Minute, model.StateWaiting, "Permission needed: apply_patch"},
		{"call answered", lines(xMeta, xStart, xCall, xOut), time.Minute, model.StateWorking, ""},
		{"turn complete", lines(xMeta, xStart, xCall, xOut, xDone), time.Minute, model.StateDone, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ev, ok := ProbeCodex("rollout-x.jsonl", c.tail, now.Add(-c.age), now, popts)
			if !ok || ev.State != c.state || ev.Detail != c.detail {
				t.Fatalf("ok=%v got %s/%q, want %s/%q", ok, ev.State, ev.Detail, c.state, c.detail)
			}
			if ev.SessionID != "cx-1" || ev.Project != "/p/web" || ev.Agent != "codex" {
				t.Fatalf("identity: %+v", ev)
			}
		})
	}
}

// When the tail no longer holds session_meta, the id comes from the file
// name and the cwd from the head of the file.
func TestProbeCodexFallbacks(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "rollout-2026-11-09T12-00-00-01a1a29d-46e3-7081-ad90-4d2cb24e0147.jsonl")
	if err := os.WriteFile(p, lines(xMeta, xStart, xDone), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ev, ok := ProbeCodex(p, lines(xStart, xDone), now, now, popts)
	if !ok || ev.SessionID != "01a1a29d-46e3-7081-ad90-4d2cb24e0147" || ev.Project != "/p/web" || ev.State != model.StateDone {
		t.Fatalf("ok=%v %+v", ok, ev)
	}
}

func TestReadTail(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f.jsonl")
	os.WriteFile(p, []byte("aaaa\nbbbb\ncccc\n"), 0o644)
	got, err := ReadTail(p, 7) // starts mid "bbbb"
	if err != nil || string(got) != "cccc\n" {
		t.Fatalf("got %q, %v", got, err)
	}
	got, _ = ReadTail(p, 100)
	if string(got) != "aaaa\nbbbb\ncccc\n" {
		t.Fatalf("whole file: %q", got)
	}
}
