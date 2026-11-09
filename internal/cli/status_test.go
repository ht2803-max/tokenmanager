package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ht2803-max/tokenmanager/internal/config"
	"github.com/ht2803-max/tokenmanager/internal/status"
)

func newTestScanner() *status.Scanner { return status.NewScanner(config.Default()) }

func runStatus(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := NewRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(append([]string{"status"}, args...))
	err := root.Execute()
	return out.String(), err
}

func TestStatusCommand(t *testing.T) {
	home := isolateCLI(t)
	f := filepath.Join(home, ".claude", "projects", "-p-api", "S.jsonl")
	os.MkdirAll(filepath.Dir(f), 0o755)
	os.WriteFile(f, []byte(`{"type":"user","sessionId":"S","cwd":"/p/api","message":{"content":"hi"}}`+"\n"+
		`{"type":"assistant","sessionId":"S","cwd":"/p/api","message":{"stop_reason":"end_turn","content":[{"type":"text","text":"ok"}]}}`+"\n"), 0o644)
	mt := time.Now().Add(-time.Minute)
	os.Chtimes(f, mt, mt)

	out, err := runStatus(t, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got statusJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("bad json: %v\n%s", err, out)
	}
	if len(got.Sessions) != 1 || got.Sessions[0].State != "done" || got.Sessions[0].Project != "/p/api" || got.Sessions[0].Agent != "claude-code" {
		t.Fatalf("sessions: %+v", got.Sessions)
	}

	out, err = runStatus(t)
	if err != nil || !strings.Contains(out, "/p/api") || !strings.Contains(out, "done") {
		t.Fatalf("table: %v\n%s", err, out)
	}

	out, _ = runStatus(t, "--json", "--agent", "codex")
	if !strings.Contains(out, `"sessions": []`) {
		t.Fatalf("filter: %s", out)
	}
	if _, err := runStatus(t, "--watch", "--json"); err == nil {
		t.Fatal("--watch --json should be rejected")
	}
}

func TestStatusWatchView(t *testing.T) {
	isolateCLI(t)
	m := &statusModel{scan: newTestScanner(), now: time.Now}
	m.Init()
	v := m.View()
	if !strings.Contains(v, "AGENT STATUS") || !strings.Contains(v, "q quit") {
		t.Fatalf("view:\n%s", v)
	}
}
