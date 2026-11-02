package optimizer

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/ht2803-max/tokenmanager/internal/analytics"
	"github.com/ht2803-max/tokenmanager/internal/config"
	"github.com/ht2803-max/tokenmanager/internal/model"
	"github.com/ht2803-max/tokenmanager/internal/storage"
)

var now = time.Date(2026, 10, 31, 15, 0, 0, 0, time.UTC)

// spec is one tool call at request index at, with a result of tokens.
type spec struct {
	at     int
	tool   string // raw name, e.g. "Read" or "mcp__srv__x"
	tokens int64
}

// scenario stores one session of n requests (prompt tokens each, all cache
// reads) with the given calls and runs the optimizer over it.
func scenario(t *testing.T, n int, prompt int64, calls ...spec) []Finding {
	t.Helper()
	t.Setenv("HOME", t.TempDir()) // no global instruction files
	db, err := storage.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	var recs []model.Record
	start := now.Add(-48 * time.Hour)
	for i := 0; i < n; i++ {
		recs = append(recs, model.Record{Key: fmt.Sprint("r", i), Agent: "claude-code", Provider: "anthropic", Model: "claude-sonnet-4-5",
			SessionID: "s", Project: "proj", SourceFile: "s.jsonl", Timestamp: start.Add(time.Duration(i) * time.Minute),
			Input: 10, Output: 200, CacheRead: prompt, CacheWrite5m: 500, Accuracy: model.Actual})
	}
	if _, err := db.Insert(recs); err != nil {
		t.Fatal(err)
	}
	var tcs []model.ToolCall
	for i, c := range calls {
		tool, srv := model.NormalizeTool(c.tool)
		tcs = append(tcs, model.ToolCall{ID: fmt.Sprint("tu", i), RecordKey: fmt.Sprint("r", c.at), SessionID: "s", Agent: "claude-code",
			Timestamp: recs[c.at].Timestamp, Tool: tool, MCPServer: srv, InputBytes: 50,
			ResultBytes: c.tokens * 4, ResultTokens: c.tokens, HasResult: true})
	}
	if err := db.UpsertTools(tcs); err != nil {
		t.Fatal(err)
	}
	e := analytics.New(config.Default(), db)
	e.Now = func() time.Time { return now }
	fs, err := Analyze(e, storage.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

func kinds(fs []Finding) map[string]Finding {
	m := map[string]Finding{}
	for _, f := range fs {
		m[f.Kind] = f
	}
	return m
}

func repeat(n, from int, tool string, tokens int64) []spec {
	var out []spec
	for i := 0; i < n; i++ {
		out = append(out, spec{from + i, tool, tokens})
	}
	return out
}

func TestLargeToolResults(t *testing.T) {
	f, ok := kinds(scenario(t, 40, 20_000, repeat(8, 0, "Read", 8_000)...))["tool-large-results"]
	if !ok {
		t.Fatal("large Read results did not fire")
	}
	if f.MonthlySavings <= 0 || f.Title != "Large tool results (estimate)" {
		t.Fatalf("bad finding: %+v", f)
	}
	if _, ok := kinds(scenario(t, 40, 20_000, repeat(8, 0, "Read", 1_000)...))["tool-large-results"]; ok {
		t.Fatal("small Read results fired")
	}
}

func TestRereadResults(t *testing.T) {
	if _, ok := kinds(scenario(t, 40, 200_000, repeat(rereadMin, 0, "Read", 2_500)...))["tool-reread"]; !ok {
		t.Fatal("repeated large results did not fire")
	}
	if _, ok := kinds(scenario(t, 40, 200_000, repeat(rereadMin-1, 0, "Read", 2_500)...))["tool-reread"]; ok {
		t.Fatal("fired below the per-session count")
	}
}

func TestShellFloods(t *testing.T) {
	if _, ok := kinds(scenario(t, 40, 50_000, repeat(floodMin, 0, "Bash", 6_000)...))["tool-shell-flood"]; !ok {
		t.Fatal("Bash floods did not fire")
	}
	if _, ok := kinds(scenario(t, 40, 50_000, repeat(floodMin-1, 0, "Bash", 6_000)...))["tool-shell-flood"]; ok {
		t.Fatal("fired below the flood count")
	}
	if _, ok := kinds(scenario(t, 40, 50_000, repeat(10, 0, "Bash", 4_000)...))["tool-shell-flood"]; ok {
		t.Fatal("fired below the flood size")
	}
}

func TestMCPOverhead(t *testing.T) {
	if _, ok := kinds(scenario(t, 40, 20_000, repeat(6, 0, "mcp__browser__snapshot", 6_000)...))["tool-mcp-overhead"]; !ok {
		t.Fatal("dominant MCP server did not fire")
	}
	if _, ok := kinds(scenario(t, 40, 200_000, repeat(6, 0, "mcp__browser__snapshot", 500)...))["tool-mcp-overhead"]; ok {
		t.Fatal("small MCP server fired")
	}
	// Rarely used: one call to "jira" among enough activity.
	busy := append(repeat(mcpActive, 0, "Edit", 10), spec{0, "mcp__jira__get", 10})
	if _, ok := kinds(scenario(t, mcpActive+5, 20_000, busy...))["tool-mcp-rare"]; !ok {
		t.Fatal("rarely used MCP server did not fire")
	}
	quiet := append(repeat(20, 0, "Edit", 10), spec{0, "mcp__jira__get", 10})
	if _, ok := kinds(scenario(t, 30, 20_000, quiet...))["tool-mcp-rare"]; ok {
		t.Fatal("rare-server finding fired without enough activity")
	}
}

func TestCarriedContext(t *testing.T) {
	// Results early in a long session ride along for ~60 requests.
	if _, ok := kinds(scenario(t, 80, 10_000, repeat(4, 0, "Edit", 1_500)...))["tool-carried-context"]; !ok {
		t.Fatal("carried context did not fire")
	}
	// Short session: results are carried for too few requests.
	if _, ok := kinds(scenario(t, 12, 3_000, repeat(4, 0, "Edit", 1_500)...))["tool-carried-context"]; ok {
		t.Fatal("fired for a short session")
	}
}
