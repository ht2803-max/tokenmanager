package collector

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ht2803-max/tokenmanager/internal/config"
	"github.com/ht2803-max/tokenmanager/internal/model"
	"github.com/ht2803-max/tokenmanager/internal/storage"
)

func line(id, req string) string {
	return `{"type":"assistant","cwd":"/w","sessionId":"s","requestId":"` + req + `","timestamp":"2026-10-31T10:00:00Z","message":{"id":"` + id + `","model":"claude-opus-5-5","usage":{"input_tokens":1,"output_tokens":2}}}` + "\n"
}

func setup(t *testing.T) (*Collector, *storage.DB, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	off := false
	for _, a := range []string{"codex", "gemini-cli", "aider", "continue", "cursor"} {
		cfg.Agents[a] = config.AgentConfig{Enabled: &off}
	}
	cfg.Agents["claude-code"] = config.AgentConfig{Paths: []string{dir}}
	db, err := storage.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return New(cfg, db, nil), db, dir
}

func TestIncrementalScanAndDedup(t *testing.T) {
	c, db, dir := setup(t)
	f := filepath.Join(dir, "proj", "a.jsonl")
	os.MkdirAll(filepath.Dir(f), 0o755)
	os.WriteFile(f, []byte(line("m1", "r1")+line("m1", "r1")+line("m2", "r2")), 0o644)

	if res := c.Scan(); len(res.Added) != 2 {
		t.Fatalf("first scan added %d, want 2", len(res.Added))
	}
	if res := c.Scan(); len(res.Added) != 0 || res.Changed != 0 {
		t.Fatalf("unchanged file re-read: %+v", res)
	}

	// Append a new request plus a partial line still being written.
	fh, _ := os.OpenFile(f, os.O_APPEND|os.O_WRONLY, 0)
	fh.WriteString(line("m3", "r3") + `{"type":"assistant","partial`)
	fh.Close()
	if res := c.Scan(); len(res.Added) != 1 {
		t.Fatalf("append scan added %d, want 1", len(res.Added))
	}
	// Completing the partial line later is picked up.
	fh, _ = os.OpenFile(f, os.O_APPEND|os.O_WRONLY, 0)
	fh.WriteString("\n" + line("m4", "r4"))
	fh.Close()
	if res := c.Scan(); len(res.Added) != 1 {
		t.Fatalf("after partial line: added %d, want 1", len(res.Added))
	}

	// A resumed session copying old history into a new file is not double counted.
	g := filepath.Join(dir, "proj", "b.jsonl")
	os.WriteFile(g, []byte(line("m1", "r1")+line("m2", "r2")+line("m5", "r5")), 0o644)
	if res := c.Scan(); len(res.Added) != 1 {
		t.Fatalf("copied history: added %d, want 1", len(res.Added))
	}

	// Truncation/rewrite resets the offset without duplicating.
	os.WriteFile(f, []byte(line("m1", "r1")), 0o644)
	if res := c.Scan(); len(res.Added) != 0 {
		t.Fatalf("rewrite added %d, want 0", len(res.Added))
	}
	st, _ := db.Stats()
	if st.Records != 5 {
		t.Fatalf("db holds %d records, want 5", st.Records)
	}
}

func TestWatchDeliversNewRecords(t *testing.T) {
	c, _, dir := setup(t)
	f := filepath.Join(dir, "proj", "live.jsonl")
	os.MkdirAll(filepath.Dir(f), 0o755)
	os.WriteFile(f, []byte(line("w1", "r1")), 0o644)
	c.Scan()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan []model.Record, 4)
	go c.Watch(ctx, ch)
	time.Sleep(200 * time.Millisecond) // let watches register

	fh, _ := os.OpenFile(f, os.O_APPEND|os.O_WRONLY, 0)
	fh.WriteString(line("w2", "r2"))
	fh.Close()
	// A brand-new project directory is picked up too.
	g := filepath.Join(dir, "newproj", "s.jsonl")
	os.MkdirAll(filepath.Dir(g), 0o755)
	os.WriteFile(g, []byte(line("w3", "r3")), 0o644)

	got := map[string]bool{}
	timeout := time.After(5 * time.Second)
	for len(got) < 2 {
		select {
		case recs := <-ch:
			for _, r := range recs {
				got[r.Key] = true
			}
		case <-timeout:
			t.Fatalf("watch delivered %v, want w2 and w3", got)
		}
	}
	if got["claude:w1:r1"] {
		t.Fatal("watch re-delivered an already ingested record")
	}
}
