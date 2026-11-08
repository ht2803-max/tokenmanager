package analytics

import (
	"fmt"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/ht2803-max/tokenmanager/internal/config"
	"github.com/ht2803-max/tokenmanager/internal/model"
	"github.com/ht2803-max/tokenmanager/internal/storage"
)

func toolEngine(t *testing.T) *Engine {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return New(config.Default(), db)
}

var tt0 = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

// stream builds n requests in one session file with the given prompt sizes
// (all cache reads, so carried tokens price at the cache-read rate).
func stream(session string, prompts ...int64) []model.Record {
	var out []model.Record
	for i, p := range prompts {
		out = append(out, model.Record{Key: fmt.Sprintf("%s-%d", session, i), Agent: "claude-code", Model: "claude-sonnet-4-5",
			SessionID: session, SourceFile: session + ".jsonl", Timestamp: tt0.Add(time.Duration(i) * time.Minute),
			Input: 10, Output: 1000, CacheRead: p, CacheWrite5m: 100})
	}
	return out
}

func call(id, rec string, in, resultTokens int64) model.ToolCall {
	return model.ToolCall{ID: id, RecordKey: rec, SessionID: "s", Agent: "claude-code", Tool: "Read",
		InputBytes: in, ResultTokens: resultTokens, ResultBytes: resultTokens * 4, HasResult: resultTokens > 0}
}

func TestOutputSplit(t *testing.T) {
	e := toolEngine(t)
	recs := stream("s", 1000, 1000, 1000)
	// Byte-weighted: 300 vs 100 input bytes split 1000 output tokens 750/250.
	uses := e.AttributeTools(recs, []model.ToolCall{call("a", "s-0", 300, 0), call("b", "s-0", 100, 0)})
	if uses[0].OutputTokens != 750 || uses[1].OutputTokens != 250 {
		t.Fatalf("weighted split: %d / %d", uses[0].OutputTokens, uses[1].OutputTokens)
	}
	// No sizes known: even split.
	uses = e.AttributeTools(recs, []model.ToolCall{call("a", "s-1", 0, 0), call("b", "s-1", 0, 0), call("c", "s-1", 0, 0)})
	for _, u := range uses {
		if u.OutputTokens != 333 {
			t.Fatalf("even split: %d", u.OutputTokens)
		}
	}
}

func TestResultChargedToNextRequestAndCarried(t *testing.T) {
	e := toolEngine(t)
	recs := stream("s", 1000, 2000, 3000, 4000, 5000)
	p, _ := e.Prices.Lookup("claude-sonnet-4-5")
	uses := e.AttributeTools(recs, []model.ToolCall{call("a", "s-1", 10, 1000)})
	u := uses[0]
	if !u.Charged {
		t.Fatal("result must be charged to the next request")
	}
	// Next request (s-2) wrote to the cache: first-time tokens at the 5m write rate.
	if want := 1000 * p.CacheWrite5m / 1e6; math.Abs(u.ResultCost-want) > 1e-9 {
		t.Fatalf("result cost %g, want %g", u.ResultCost, want)
	}
	// Carried by s-3 and s-4, then the session ends.
	if u.CarriedReqs != 2 || u.CarriedTokens != 2000 {
		t.Fatalf("carried %d requests / %d tokens, want 2 / 2000", u.CarriedReqs, u.CarriedTokens)
	}
	if want := 2000 * p.CacheRead / 1e6; math.Abs(u.CarriedCost-want) > 1e-9 {
		t.Fatalf("carried cost %g, want %g", u.CarriedCost, want)
	}

	// A result from the last request of a session never entered a prompt.
	uses = e.AttributeTools(recs, []model.ToolCall{call("b", "s-4", 10, 1000)})
	if uses[0].Charged || uses[0].CarriedTokens != 0 || uses[0].ResultCost != 0 {
		t.Fatalf("result after the session end was charged: %+v", uses[0])
	}

	// Another session (or a subagent file) does not carry it.
	other := stream("t", 1000, 1000, 1000)
	uses = e.AttributeTools(append(recs, other...), []model.ToolCall{call("c", "s-1", 10, 1000)})
	if uses[0].CarriedReqs != 2 {
		t.Fatalf("carry leaked across sessions: %d", uses[0].CarriedReqs)
	}
}

func TestCarryStopsAtCompaction(t *testing.T) {
	e := toolEngine(t)
	// The prompt drops from 90k to 20k at s-3: a compaction.
	recs := stream("s", 50_000, 70_000, 90_000, 20_000, 25_000)
	uses := e.AttributeTools(recs, []model.ToolCall{call("a", "s-0", 10, 1000)})
	if uses[0].CarriedReqs != 1 {
		t.Fatalf("carried %d requests, want 1 (s-2, stopping before the compaction)", uses[0].CarriedReqs)
	}
}

func TestGroupTools(t *testing.T) {
	e := toolEngine(t)
	recs := stream("s", 1000, 1000, 1000)
	cs := []model.ToolCall{call("a", "s-0", 1, 100), call("b", "s-0", 1, 300), call("c", "s-1", 1, 0)}
	cs[2].Tool, cs[2].MCPServer = "search", "github"
	uses := e.AttributeTools(recs, cs)
	var prompt int64
	for _, r := range recs {
		prompt += r.PromptTokens()
	}
	gs := GroupTools(uses, ByTool, prompt)
	if len(gs) != 2 {
		t.Fatalf("got %d groups", len(gs))
	}
	var read *ToolStat
	for _, g := range gs {
		if g.Key == "Read" {
			read = g
		}
	}
	if read == nil || read.Calls != 2 || read.Results != 2 || read.AvgResult != 200 || read.P95Result != 300 || read.FirstTokens != 400 {
		t.Fatalf("bad Read stats: %+v", read)
	}
	if want := float64(read.InputTokens()) / float64(prompt); read.Share != want {
		t.Fatalf("share %g, want %g", read.Share, want)
	}
	srv := GroupTools(uses, ByMCPServer, prompt)
	if len(srv) != 1 || srv[0].Key != "github" || srv[0].Calls != 1 {
		t.Fatalf("by server: %+v", srv)
	}
}

func TestPercentile(t *testing.T) {
	v := []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20}
	if p := Percentile(v, 0.95); p != 19 {
		t.Fatalf("p95 = %d", p)
	}
	if Percentile(nil, 0.95) != 0 || Percentile([]int64{7}, 0.95) != 7 {
		t.Fatal("edge cases")
	}
}
