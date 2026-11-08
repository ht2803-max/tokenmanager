package collector

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/ht2803-max/tokenmanager/internal/config"
	"github.com/ht2803-max/tokenmanager/internal/storage"
)

func toolUse(msg, req, id, name string) string {
	return fmt.Sprintf(`{"type":"assistant","sessionId":"s","requestId":%q,"timestamp":"2026-10-31T10:00:00Z","message":{"id":%q,"model":"claude-opus-5-5","content":[{"type":"tool_use","id":%q,"name":%q,"input":{"x":1}}],"usage":{"input_tokens":1,"output_tokens":2}}}`+"\n", req, msg, id, name)
}

func toolRes(id, body string) string {
	return fmt.Sprintf(`{"type":"user","sessionId":"s","timestamp":"2026-10-31T10:00:01Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":%q,"content":%q}]}}`+"\n", id, body)
}

func calls(t *testing.T, db *storage.DB) map[string][2]int64 {
	t.Helper()
	cs, err := db.QueryTools(storage.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][2]int64{}
	for _, c := range cs {
		if _, dup := out[c.ID]; dup {
			t.Fatalf("duplicate tool row %s", c.ID)
		}
		out[c.ID] = [2]int64{c.ResultBytes, map[bool]int64{true: 1}[c.RecordKey != ""]}
	}
	return out
}

func TestToolRowsMergeAcrossReadsAndCopies(t *testing.T) {
	c, db, dir := setup(t)
	f := filepath.Join(dir, "proj", "a.jsonl")
	os.MkdirAll(filepath.Dir(f), 0o755)
	// Two parallel calls of one request, split over two lines.
	os.WriteFile(f, []byte(toolUse("m1", "r1", "tu1", "Read")+toolUse("m1", "r1", "tu2", "Grep")), 0o644)
	c.Scan()
	if got := calls(t, db); len(got) != 2 || got["tu1"][0] != 0 || got["tu1"][1] != 1 {
		t.Fatalf("calls before results: %v", got)
	}

	// Results arrive in a later incremental read.
	fh, _ := os.OpenFile(f, os.O_APPEND|os.O_WRONLY, 0)
	fh.WriteString(toolRes("tu1", "0123456789") + toolRes("tu2", "abc"))
	fh.Close()
	c.Scan()
	if got := calls(t, db); got["tu1"][0] != 10 || got["tu2"][0] != 3 {
		t.Fatalf("results not merged into calls: %v", got)
	}

	// A resumed session copies the history (calls and results) into a new
	// file, then continues: nothing is double counted.
	g := filepath.Join(dir, "proj", "b.jsonl")
	os.WriteFile(g, []byte(toolUse("m1", "r1", "tu1", "Read")+toolRes("tu1", "0123456789")+toolUse("m2", "r2", "tu3", "Bash")+toolRes("tu3", "xy")), 0o644)
	c.Scan()
	got := calls(t, db)
	if len(got) != 3 || got["tu1"][0] != 10 || got["tu3"][0] != 2 {
		t.Fatalf("copied history double counted or lost: %v", got)
	}

	// Re-reading everything (scan --rebuild style) is idempotent.
	for _, p := range []string{f, g} {
		fs, _ := db.GetFile(p)
		fs.Offset, fs.Size = 0, 0
		db.PutFile(fs)
	}
	c.Scan()
	if again := calls(t, db); len(again) != 3 || again["tu1"] != got["tu1"] {
		t.Fatalf("re-read changed tool rows: %v", again)
	}
}

// A database written before tool calls existed has its files read past
// their offsets; the one-time backfill re-reads them without losing records.
func TestToolBackfillKeepsRecords(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	off := false
	for _, a := range []string{"codex", "gemini-cli", "aider", "continue", "cursor"} {
		cfg.Agents[a] = config.AgentConfig{Enabled: &off}
	}
	cfg.Agents["claude-code"] = config.AgentConfig{Paths: []string{dir}}
	path := filepath.Join(t.TempDir(), "t.db")
	db, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	c := New(cfg, db, nil)

	f := filepath.Join(dir, "proj", "a.jsonl")
	os.MkdirAll(filepath.Dir(f), 0o755)
	os.WriteFile(f, []byte(toolUse("m1", "r1", "tu1", "Read")+toolRes("tu1", "abcd")), 0o644)
	gone := filepath.Join(dir, "proj", "deleted.jsonl")
	os.WriteFile(gone, []byte(line("old", "r0")), 0o644)
	c.Scan()
	os.Remove(gone) // the agent cleaned up an old transcript

	// Turn the database back into what an older version left behind.
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`DELETE FROM tool_calls; DELETE FROM kv WHERE k LIKE 'reparsed:%'`); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	res := c.Scan()
	if res.Reparsed != 1 {
		t.Fatalf("backfill re-read %d files, want 1", res.Reparsed)
	}
	if got := calls(t, db); got["tu1"][0] != 4 {
		t.Fatalf("backfill did not restore tool rows: %v", got)
	}
	if st, _ := db.Stats(); st.Records != 2 {
		t.Fatalf("backfill must keep records whose files are gone: %d records", st.Records)
	}
	if res := c.Scan(); res.Reparsed != 0 {
		t.Fatalf("backfill ran twice: %+v", res)
	}
}
