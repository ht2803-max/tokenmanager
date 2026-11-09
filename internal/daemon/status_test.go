package daemon

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ht2803-max/tokenmanager/internal/config"
	"github.com/ht2803-max/tokenmanager/internal/model"
)

const (
	tPrompt = `{"type":"user","sessionId":"S","cwd":"/p/api","message":{"content":"hi"}}`
	tTool   = `{"type":"assistant","sessionId":"S","cwd":"/p/api","message":{"stop_reason":"tool_use","content":[{"type":"tool_use","id":"t1","name":"Bash"}]}}`
)

func TestStatusWatchNotifiesOncePerEpisode(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	off := false
	for _, a := range []string{"codex", "gemini-cli", "aider", "continue", "cursor"} {
		cfg.Agents[a] = config.AgentConfig{Enabled: &off}
	}
	cfg.Agents["claude-code"] = config.AgentConfig{Paths: []string{dir}}
	w := newStatusWatch(cfg)
	now := time.Now()
	w.now = func() time.Time { return now }
	w.scan.Now = w.now
	var sent []string
	w.notify = func(title, msg string) error { sent = append(sent, title+" | "+msg); return nil }
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	f := filepath.Join(dir, "p", "S.jsonl")
	os.MkdirAll(filepath.Dir(f), 0o755)
	os.WriteFile(f, []byte(tPrompt+"\n"+tTool+"\n"), 0o644)
	os.Chtimes(f, now, now)

	w.check(log) // seed: working
	w.check(log)
	if len(sent) != 0 {
		t.Fatalf("notified while working: %v", sent)
	}
	now = now.Add(10 * time.Second) // still under the 20s default
	w.check(log)
	if len(sent) != 0 {
		t.Fatalf("notified before waiting_after: %v", sent)
	}
	now = now.Add(11 * time.Second) // tool unanswered past waiting_after
	w.check(log)
	w.check(log)
	if len(sent) != 1 || !strings.Contains(sent[0], "api · Claude Code") || !strings.Contains(sent[0], "Probably waiting for you: Permission needed: Bash") {
		t.Fatalf("want one waiting notification, got %v", sent)
	}

	cfg.Status.MuteProjects = []string{"/p/api"}
	os.Chtimes(f, now, now) // back to working, then waiting again, but muted
	w.check(log)
	now = now.Add(21 * time.Second)
	w.check(log)
	if len(sent) != 1 {
		t.Fatalf("muted project notified: %v", sent)
	}
}

func TestStatusMessageMany(t *testing.T) {
	evs := []model.StatusEvent{
		{Agent: "claude-code", Project: "/x/api", Confidence: model.High},
		{Agent: "codex", Project: "/x/web", Confidence: model.High},
	}
	title, msg := StatusMessage(evs, "waiting")
	if title != "2 agents waiting for you" || msg != "api, web" {
		t.Fatalf("%q %q", title, msg)
	}
}
