package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Strings that appear only inside tool inputs and results. None of them may
// reach the database, a JSON/CSV export or the HTML dashboard.
var secrets = []string{
	"sk-ant-api03-FAKEKEYdoNotStore0123456789",
	"/Users/victim/secret-project/credentials.env",
	"curl -H 'Authorization: Bearer tok_SUPERSECRET' https://internal.example.com/admin",
	"tok_SUPERSECRET",
	"internal.example.com",
	"AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMIK7MDENGbPxRfiCYEXAMPLEKEY",
	"ghp_FakeGithubToken000000000000000000000",
}

func runCLI(t *testing.T, args ...string) string {
	t.Helper()
	root := NewRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader(""))
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out.String())
	}
	return out.String()
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestToolDataPrivacy(t *testing.T) {
	home := isolateCLI(t)
	ts := func(m int) string {
		return time.Now().Add(time.Duration(m-90) * time.Minute).UTC().Format(time.RFC3339)
	}
	esc := func(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) }

	// Claude Code: secrets in a Bash command, a Read path, results and the toolUseResult sidecar.
	claude := fmt.Sprintf(`{"type":"assistant","cwd":"/w/app","sessionId":"cs1","requestId":"q1","timestamp":%q,"message":{"id":"m1","model":"claude-sonnet-4-5","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"%s"}}],"usage":{"input_tokens":5,"output_tokens":50,"cache_read_input_tokens":1000}}}
{"type":"assistant","cwd":"/w/app","sessionId":"cs1","requestId":"q1","timestamp":%q,"message":{"id":"m1","model":"claude-sonnet-4-5","content":[{"type":"tool_use","id":"toolu_2","name":"Read","input":{"file_path":"%s"}}],"usage":{"input_tokens":5,"output_tokens":50,"cache_read_input_tokens":1000}}}
{"type":"user","cwd":"/w/app","sessionId":"cs1","timestamp":%q,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"%s"}]},"toolUseResult":{"stdout":"%s"}}
{"type":"user","cwd":"/w/app","sessionId":"cs1","timestamp":%q,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_2","content":[{"type":"text","text":"%s\n%s"}]}]},"toolUseResult":{"filePath":"%s"}}
{"type":"assistant","cwd":"/w/app","sessionId":"cs1","requestId":"q2","timestamp":%q,"message":{"id":"m2","model":"claude-sonnet-4-5","content":[{"type":"tool_use","id":"toolu_3","name":"mcp__github__get_file","input":{"token":"%s"}}],"usage":{"input_tokens":5,"output_tokens":20,"cache_read_input_tokens":3000}}}
{"type":"user","cwd":"/w/app","sessionId":"cs1","timestamp":%q,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_3","content":"%s"}]}}
{"type":"assistant","cwd":"/w/app","sessionId":"cs1","requestId":"q3","timestamp":%q,"message":{"id":"m3","model":"claude-sonnet-4-5","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":5,"output_tokens":20,"cache_read_input_tokens":4000}}}
`, ts(1), esc(secrets[2]), ts(2), secrets[1], ts(3), secrets[0], secrets[0], ts(4), secrets[5], secrets[1], secrets[1],
		ts(5), secrets[6], ts(6), secrets[6], ts(7))
	writeFile(t, filepath.Join(home, ".claude", "projects", "-w-app", "cs1.jsonl"), claude)

	// Codex: secrets in an exec input and its output.
	codex := fmt.Sprintf(`{"timestamp":%q,"type":"session_meta","payload":{"id":"cx1","cwd":"/w/cx"}}
{"timestamp":%q,"type":"turn_context","payload":{"model":"gpt-5"}}
{"timestamp":%q,"type":"response_item","payload":{"type":"custom_tool_call","call_id":"call_1","name":"exec","input":"%s"}}
{"timestamp":%q,"type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"call_1","output":[{"type":"input_text","text":"%s"}]}}
{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":1100},"last_token_usage":{"input_tokens":1000,"output_tokens":100,"total_tokens":1100}}}}
{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":2300},"last_token_usage":{"input_tokens":1150,"output_tokens":50,"total_tokens":1200}}}}
`, ts(1), ts(1), ts(2), esc(secrets[2]), ts(3), secrets[5], ts(4), ts(5))
	writeFile(t, filepath.Join(home, ".codex", "sessions", "2026", "11", "02", "rollout-2026-11-02T10-00-00-0199aaaa-bbbb-cccc-dddd-eeeeeeeeeeee.jsonl"), codex)

	// Gemini CLI: secrets in args and the function response.
	gemini := fmt.Sprintf(`{"sessionId":"g1","projectHash":"abcdef0123456789","messages":[{"id":"gm1","timestamp":%q,"type":"gemini","model":"gemini-2.5-pro","tokens":{"input":500,"output":40},
"toolCalls":[{"id":"rf-1","name":"read_file","args":{"absolute_path":%q},"result":[{"functionResponse":{"id":"rf-1","name":"read_file","response":{"output":%q}}}],"status":"success"}]}]}`,
		ts(2), secrets[1], secrets[0])
	writeFile(t, filepath.Join(home, ".gemini", "tmp", "abcdef", "chats", "session-1.json"), gemini)

	outputs := map[string]string{
		"tools --json":      runCLI(t, "tools", "--json", "--all"),
		"tools --csv":       runCLI(t, "tools", "--csv", "--all"),
		"tools --by server": runCLI(t, "tools", "--by", "server", "--json", "--all"),
		"export json":       runCLI(t, "export", "-f", "json"),
		"export csv":        runCLI(t, "export", "-f", "csv"),
		"optimize --json":   runCLI(t, "optimize", "--json"),
		"sessions --json":   runCLI(t, "sessions", "--json", "--all"),
	}
	page := filepath.Join(home, "dash.html")
	runCLI(t, "web", "--no-scan", "-o", page)
	html, err := os.ReadFile(page)
	if err != nil {
		t.Fatal(err)
	}
	outputs["web html"] = string(html)

	// Not vacuous: every agent's calls made it through.
	for _, want := range []string{`"Bash"`, `"Read"`, `"github/get_file"`, `"exec"`, `"read_file"`} {
		if !strings.Contains(outputs["tools --json"], want) {
			t.Fatalf("tools --json lacks %s:\n%s", want, outputs["tools --json"])
		}
	}
	if !strings.Contains(outputs["web html"], `"github/get_file"`) {
		t.Fatal("web export lacks the tools table data")
	}

	// Every file in the data dir: the database, its WAL and shm.
	dataDir := filepath.Join(home, "data", "tokenmanager")
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dataDir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		outputs["data/"+e.Name()] = string(b)
	}
	if _, ok := outputs["data/usage.db"]; !ok {
		t.Fatal("database file not found")
	}

	for name, out := range outputs {
		for _, s := range secrets {
			if strings.Contains(out, s) {
				t.Errorf("%s contains secret %q", name, s)
			}
		}
	}
}
