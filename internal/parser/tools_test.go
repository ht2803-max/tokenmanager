package parser

import (
	"strings"
	"testing"

	"github.com/ht2803-max/tokenmanager/internal/model"
)

// One request (msg_1/req_1) whose content blocks are spread over three
// lines: thinking, then two parallel tool_use blocks. Results come back on
// later user lines; one is an image.
const claudeToolsFixture = `{"type":"assistant","sessionId":"s1","requestId":"req_1","timestamp":"2026-10-31T10:00:01Z","message":{"id":"msg_1","model":"claude-opus-5-5","content":[{"type":"thinking","thinking":"hmm"}],"usage":{"input_tokens":3,"output_tokens":90}}}
{"type":"assistant","sessionId":"s1","requestId":"req_1","timestamp":"2026-10-31T10:00:02Z","message":{"id":"msg_1","model":"claude-opus-5-5","content":[{"type":"tool_use","id":"toolu_A","name":"Read","input":{"file_path":"/secret/path.go"}}],"usage":{"input_tokens":3,"output_tokens":90}}}
{"type":"assistant","sessionId":"s1","requestId":"req_1","timestamp":"2026-10-31T10:00:02Z","message":{"id":"msg_1","model":"claude-opus-5-5","content":[{"type":"tool_use","id":"toolu_B","name":"mcp__github__search_issues","input":{"q":"x"}}],"usage":{"input_tokens":3,"output_tokens":90}}}
{"type":"user","sessionId":"s1","timestamp":"2026-10-31T10:00:03Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_A","content":"0123456789abcdef"}]},"toolUseResult":{"filePath":"/secret/path.go"}}
{"type":"user","sessionId":"s1","timestamp":"2026-10-31T10:00:03Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_B","content":[{"type":"text","text":"12345678"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}}]}]}}
`

func TestClaudeToolCalls(t *testing.T) {
	recs, tools, _, err := ParseClaudeTools(strings.NewReader(claudeToolsFixture), "f.jsonl", State{})
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 3 || recs[0].Key != recs[2].Key {
		t.Fatalf("lines of one request must share a key: %d records", len(recs))
	}
	if len(tools) != 4 {
		t.Fatalf("got %d tool rows, want 2 calls + 2 results", len(tools))
	}
	a, b, ra, rb := tools[0], tools[1], tools[2], tools[3]
	if a.ID != "toolu_A" || a.Tool != "Read" || a.MCPServer != "" || a.RecordKey != recs[0].Key || a.InputBytes == 0 {
		t.Fatalf("bad Read call: %+v", a)
	}
	if b.Tool != "search_issues" || b.MCPServer != "github" || b.RecordKey != recs[0].Key {
		t.Fatalf("MCP name not split: %+v", b)
	}
	if ra.ID != "toolu_A" || !ra.HasResult || ra.ResultBytes != 16 || ra.ResultTokens != 4 || ra.Tool != "" {
		t.Fatalf("bad string result: %+v", ra)
	}
	// 8 text bytes + one image: base64 bytes and media types do not count.
	if rb.ResultBytes != 8 || rb.ResultTokens != 2+model.ImageTokens {
		t.Fatalf("bad image result: %+v", rb)
	}
}

func TestClaudeResultInLaterRead(t *testing.T) {
	lines := strings.SplitAfter(claudeToolsFixture, "\n")
	st := State{}
	_, first, _, _ := ParseClaudeTools(strings.NewReader(strings.Join(lines[:3], "")), "f.jsonl", st)
	_, second, _, _ := ParseClaudeTools(strings.NewReader(strings.Join(lines[3:], "")), "f.jsonl", st)
	if len(first) != 2 || len(second) != 2 || second[0].ID != first[0].ID || !second[0].HasResult {
		t.Fatalf("result rows must carry the call id across reads: %+v / %+v", first, second)
	}
	if len(st) > 1 {
		t.Fatalf("Claude tool linking must not grow parser state: %v", st)
	}
}

func TestClaudeSidechainToolCalls(t *testing.T) {
	sub := `{"type":"assistant","isSidechain":true,"agentId":"a1","sessionId":"s1","requestId":"req_9","timestamp":"2026-10-31T10:05:00Z","message":{"id":"msg_9","model":"claude-haiku-5-5","content":[{"type":"tool_use","id":"toolu_S","name":"Grep","input":{"pattern":"x"}}],"usage":{"input_tokens":5,"output_tokens":7}}}
{"type":"user","isSidechain":true,"sessionId":"s1","timestamp":"2026-10-31T10:05:01Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_S","content":"abcd"}]}}
`
	recs, tools, _, _ := ParseClaudeTools(strings.NewReader(sub), "s1/subagents/agent-a1.jsonl", State{})
	if len(recs) != 1 || len(tools) != 2 || tools[0].RecordKey != recs[0].Key || tools[0].SessionID != "s1" || tools[1].ResultBytes != 4 {
		t.Fatalf("subagent calls must link to the subagent's request: %+v %+v", recs, tools)
	}
}

func TestNormalizeTool(t *testing.T) {
	cases := []struct{ in, tool, srv string }{
		{"Read", "Read", ""},
		{"mcp__claude-in-chrome__navigate", "navigate", "claude-in-chrome"},
		{"mcp__srv__a__b", "a__b", "srv"},
		{"mcp__broken", model.OtherTool, ""},
		{"", model.OtherTool, ""},
		{"rm -rf /tmp/x", model.OtherTool, ""},
		{strings.Repeat("x", 200), model.OtherTool, ""},
	}
	for _, c := range cases {
		if tool, srv := model.NormalizeTool(c.in); tool != c.tool || srv != c.srv {
			t.Errorf("NormalizeTool(%q) = %q, %q; want %q, %q", c.in, tool, srv, c.tool, c.srv)
		}
	}
}

// Calls precede their outputs, and both precede the token_count of the
// request that emitted them; the token_count may fall in a later read.
const codexToolsFixture = `{"timestamp":"2026-10-29T02:26:26Z","type":"session_meta","payload":{"id":"sess-1","cwd":"/w"}}
{"timestamp":"2026-10-29T02:26:27Z","type":"turn_context","payload":{"model":"gpt-5"}}
{"timestamp":"2026-10-29T02:26:28Z","type":"response_item","payload":{"type":"custom_tool_call","call_id":"call_1","name":"exec","input":"cat /etc/secret"}}
{"timestamp":"2026-10-29T02:26:28Z","type":"response_item","payload":{"type":"function_call","call_id":"call_2","name":"github__list_prs","arguments":"{}"}}
{"timestamp":"2026-10-29T02:26:29Z","type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"call_1","output":[{"type":"input_text","text":"abcdefgh"}]}}
{"timestamp":"2026-10-29T02:26:29Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call_2","output":"1234"}}
`

const codexTokenCount = `{"timestamp":"2026-10-29T02:26:30Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":1050},"last_token_usage":{"input_tokens":1000,"output_tokens":50,"total_tokens":1050}}}}
`

func TestCodexToolLinking(t *testing.T) {
	st := State{}
	recs, tools, _, err := ParseCodexTools(strings.NewReader(codexToolsFixture), "rollout-x.jsonl", st)
	if err != nil || len(recs) != 0 || len(tools) != 4 {
		t.Fatalf("first read: %v, %d records, %d tools", err, len(recs), len(tools))
	}
	if tools[0].Tool != "exec" || tools[0].RecordKey != "" || tools[1].Tool != "list_prs" || tools[1].MCPServer != "github" {
		t.Fatalf("bad calls: %+v", tools[:2])
	}
	if tools[2].ID != tools[0].ID || tools[2].ResultBytes != 8 || tools[3].ResultBytes != 4 {
		t.Fatalf("outputs must link by call_id: %+v", tools[2:])
	}
	if st[codexPending] == "" {
		t.Fatal("pending call ids must survive the read boundary")
	}
	recs, links, _, _ := ParseCodexTools(strings.NewReader(codexTokenCount), "rollout-x.jsonl", st)
	if len(recs) != 1 || len(links) != 2 || links[0].RecordKey != recs[0].Key || links[0].ID != tools[0].ID || links[1].ID != tools[1].ID {
		t.Fatalf("token_count must link pending calls: %+v %+v", recs, links)
	}
	if _, ok := st[codexPending]; ok {
		t.Fatal("linked ids must be pruned from state")
	}
}

func TestCodexPendingCapped(t *testing.T) {
	var b strings.Builder
	for i := 0; i < maxPending+50; i++ {
		b.WriteString(`{"timestamp":"2026-10-29T02:26:28Z","type":"response_item","payload":{"type":"function_call","call_id":"c` + strings.Repeat("x", 3) + string(rune('a'+i%26)) + `","name":"f"}}` + "\n")
	}
	st := State{}
	ParseCodexTools(strings.NewReader(b.String()), "rollout-x.jsonl", st)
	if n := len(strings.Fields(st[codexPending])); n > maxPending {
		t.Fatalf("pending list grew to %d", n)
	}
}

func TestGeminiToolCalls(t *testing.T) {
	doc := `{"sessionId":"g1","messages":[{"id":"m1","timestamp":"2026-10-30T09:00:00Z","type":"gemini","model":"gemini-3-pro","tokens":{"input":100,"output":10},
	"toolCalls":[{"id":"read_file-1","name":"read_file","args":{"absolute_path":"/secret"},"result":[{"functionResponse":{"id":"read_file-1","name":"read_file","response":{"output":"abcdefgh"}}}],"status":"success"}]}]}`
	recs, tools, _, err := ParseGeminiTools(strings.NewReader(doc), "chats/session-1.json", State{})
	if err != nil || len(recs) != 1 || len(tools) != 1 {
		t.Fatalf("%v %d %d", err, len(recs), len(tools))
	}
	if c := tools[0]; c.Tool != "read_file" || c.RecordKey != recs[0].Key || !c.HasResult || c.ResultBytes != 8 {
		t.Fatalf("bad gemini call: %+v", c)
	}
}
