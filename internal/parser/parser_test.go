package parser

import (
	"strings"
	"testing"
)

const claudeFixture = `{"type":"user","cwd":"/work/app","sessionId":"s1","timestamp":"2026-10-31T10:00:00Z","message":{"role":"user","content":"hi"}}
{"type":"assistant","cwd":"/work/app","sessionId":"s1","requestId":"req_1","timestamp":"2026-10-31T10:00:02Z","message":{"id":"msg_1","model":"claude-opus-5-5","usage":{"input_tokens":3,"output_tokens":50,"cache_read_input_tokens":1000,"cache_creation_input_tokens":200,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":200},"output_tokens_details":{"thinking_tokens":10}}}}
{"type":"assistant","cwd":"/work/app","sessionId":"s1","requestId":"req_1","timestamp":"2026-10-31T10:00:03Z","message":{"id":"msg_1","model":"claude-opus-5-5","usage":{"input_tokens":3,"output_tokens":50,"cache_read_input_tokens":1000,"cache_creation_input_tokens":200,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":200}}}}
{"type":"assistant","sessionId":"s1","timestamp":"2026-10-31T10:00:04Z","message":{"id":"msg_x","model":"<synthetic>","usage":{"input_tokens":0,"output_tokens":0}}}
not json at all
{"type":"assistant","cwd":"/work/app","sessionId":"s1","requestId":"req_2","timestamp":"2026-10-31T10:01:00Z","message":{"id":"msg_2","model":"claude-sonnet-5-5","usage":{"input_tokens":10,"output_tokens":5,"cache_creation_input_tokens":40}}}
{"type":"assistant","sessionId":"s1","requestId":"req_3","timestamp":"2026-10-31T10:02:00Z","message":{"id":"msg_3","model":"claude-opus-5-5","usage":{"input_tokens":1`

func TestParseClaude(t *testing.T) {
	st := State{}
	recs, n, err := ParseClaude(strings.NewReader(claudeFixture), "f.jsonl", st)
	if err != nil {
		t.Fatal(err)
	}
	// The trailing partial line must not be consumed.
	if want := int64(strings.LastIndex(claudeFixture, "\n") + 1); n != want {
		t.Fatalf("consumed %d bytes, want %d", n, want)
	}
	if len(recs) != 3 {
		t.Fatalf("got %d records, want 3 (two lines share msg_1)", len(recs))
	}
	if recs[0].Key != recs[1].Key {
		t.Fatalf("content-block lines of one message must share a key: %q vs %q", recs[0].Key, recs[1].Key)
	}
	r := recs[0]
	if r.Input != 3 || r.Output != 50 || r.CacheRead != 1000 || r.CacheWrite1h != 200 || r.CacheWrite5m != 0 || r.Reasoning != 10 {
		t.Fatalf("bad token split: %+v", r)
	}
	if r.Project != "/work/app" || r.SessionID != "s1" || r.Provider != "anthropic" {
		t.Fatalf("bad metadata: %+v", r)
	}
	// Without a cache_creation breakdown the TTL is unknown and priced as 5m.
	if recs[2].CacheWrite5m != 40 || recs[2].CacheWrite1h != 0 {
		t.Fatalf("unknown TTL: %+v", recs[2])
	}
}

const codexFixture = `{"timestamp":"2026-10-29T02:26:26Z","type":"session_meta","payload":{"id":"sess-1","cwd":"/work/cx","model_provider":"openai"}}
{"timestamp":"2026-10-29T02:26:27Z","type":"turn_context","payload":{"model":"gpt-5","cwd":"/work/cx"}}
{"timestamp":"2026-10-29T02:26:28Z","type":"event_msg","payload":{"type":"token_count","info":null}}
{"timestamp":"2026-10-29T02:26:30Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":1000,"cached_input_tokens":600,"output_tokens":50,"reasoning_output_tokens":20,"total_tokens":1050},"last_token_usage":{"input_tokens":1000,"cached_input_tokens":600,"output_tokens":50,"reasoning_output_tokens":20,"total_tokens":1050},"model_context_window":258400}}}
{"timestamp":"2026-10-29T02:26:31Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":1000,"cached_input_tokens":600,"output_tokens":50,"reasoning_output_tokens":20,"total_tokens":1050},"last_token_usage":{"input_tokens":1000,"cached_input_tokens":600,"output_tokens":50,"reasoning_output_tokens":20,"total_tokens":1050},"model_context_window":258400}}}
`

func TestParseCodex(t *testing.T) {
	st := State{}
	recs, _, err := ParseCodex(strings.NewReader(codexFixture), "rollout-x.jsonl", st)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 || recs[0].Key != recs[1].Key {
		t.Fatalf("duplicate token_count events must share a key: %+v", recs)
	}
	r := recs[0]
	if r.Input != 400 || r.CacheRead != 600 || r.Output != 50 || r.Reasoning != 20 {
		t.Fatalf("cached tokens must be split out of input: %+v", r)
	}
	if r.Model != "gpt-5" || r.SessionID != "sess-1" || r.Project != "/work/cx" || r.ContextWindow != 258400 {
		t.Fatalf("bad metadata: %+v", r)
	}

	// Incremental parse: state from the first chunk carries over.
	more := `{"timestamp":"2026-10-29T02:27:00Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":1300},"last_token_usage":{"input_tokens":200,"output_tokens":50,"total_tokens":250}}}}` + "\n"
	recs, _, _ = ParseCodex(strings.NewReader(more), "rollout-x.jsonl", st)
	if len(recs) != 1 || recs[0].Model != "gpt-5" || recs[0].SessionID != "sess-1" || recs[0].Key == r.Key {
		t.Fatalf("incremental parse lost state: %+v", recs)
	}
}

func TestParseCodexSessionFromFilename(t *testing.T) {
	line := `{"timestamp":"2026-10-29T02:27:00Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":5,"output_tokens":5}}}}` + "\n"
	recs, _, _ := ParseCodex(strings.NewReader(line), "/x/rollout-2026-10-28T22-26-26-01a1857a-e4fd-7243-965a-464511471a77.jsonl", State{})
	if len(recs) != 1 || recs[0].SessionID != "01a1857a-e4fd-7243-965a-464511471a77" || recs[0].Model != "unknown" {
		t.Fatalf("got %+v", recs)
	}
}

func TestParseGemini(t *testing.T) {
	doc := `{"sessionId":"g1","projectHash":"abcdef1234567890","messages":[
	 {"id":"u1","timestamp":"2026-10-30T10:00:00Z","type":"user","content":"hi"},
	 {"id":"m1","timestamp":"2026-10-30T10:00:05Z","type":"gemini","model":"gemini-2.5-pro","tokens":{"input":1000,"output":100,"cached":400,"thoughts":30,"tool":0,"total":1130}}]}`
	recs, _, err := ParseGemini(strings.NewReader(doc), "session.json", State{})
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("got %d records", len(recs))
	}
	r := recs[0]
	if r.Input != 600 || r.CacheRead != 400 || r.Output != 130 || r.Project != "gemini:abcdef12" {
		t.Fatalf("got %+v", r)
	}
}

func TestParseAider(t *testing.T) {
	log := "# aider chat started at 2026-10-30 09:00:00\n\n> Main model: claude-sonnet-4-5 with diff edit format\n" +
		"> Tokens: 12k sent, 2.0k cache write, 8.0k cache hit, 1.2k received. Cost: $0.05 message, $0.05 session.\n" +
		"> Tokens: 3.5k sent, 300 received. Cost: $0.01 message, $0.06 session.\n"
	recs, _, err := ParseAider(strings.NewReader(log), "/work/a/.aider.chat.history.md", State{})
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("got %d records", len(recs))
	}
	r := recs[0]
	if r.Input != 2000 || r.CacheWrite5m != 2000 || r.CacheRead != 8000 || r.Output != 1200 || r.ReportedCost != 0.05 || r.Accuracy != "estimated" {
		t.Fatalf("got %+v", r)
	}
	if r.Project != "/work/a" || r.Provider != "anthropic" || recs[0].Key == recs[1].Key {
		t.Fatalf("got %+v", r)
	}
}

func TestParseContinue(t *testing.T) {
	log := `{"model":"gpt-4o","provider":"openai","promptTokens":100,"generatedTokens":20,"timestamp":"2026-10-30T10:00:00Z"}` + "\n" +
		`{"model":"gpt-4o","promptTokens":0,"generatedTokens":0,"timestamp":"2026-10-30T10:00:01Z"}` + "\n"
	st := State{}
	recs, _, _ := ParseContinue(strings.NewReader(log), "t.jsonl", st)
	if len(recs) != 1 || recs[0].Input != 100 || recs[0].Output != 20 {
		t.Fatalf("got %+v", recs)
	}
	// Keys stay unique across incremental reads.
	more := `{"model":"gpt-4o","promptTokens":5,"generatedTokens":5,"timestamp":"2026-10-30T10:00:00Z"}` + "\n"
	recs2, _, _ := ParseContinue(strings.NewReader(more), "t.jsonl", st)
	if len(recs2) != 1 || recs2[0].Key == recs[0].Key {
		t.Fatalf("expected distinct key, got %v / %v", recs2, recs)
	}
}
