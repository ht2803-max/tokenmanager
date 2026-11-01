package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestLimitsJSON ingests a Codex rollout with a current rate_limits snapshot
// and checks the shape of "limits --json".
func TestLimitsJSON(t *testing.T) {
	home := isolateCLI(t)
	now := time.Now().UTC()
	dir := filepath.Join(home, ".codex", "sessions", now.Format("2006/01/02"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	ts := now.Add(-10 * time.Minute).Format(time.RFC3339)
	rollout := fmt.Sprintf(`{"timestamp":%q,"type":"session_meta","payload":{"id":"s1","cwd":"/w","model_provider":"openai"}}
{"timestamp":%q,"type":"turn_context","payload":{"model":"gpt-5"}}
{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":1100},"last_token_usage":{"input_tokens":1000,"cached_input_tokens":600,"output_tokens":100,"total_tokens":1100}},"rate_limits":{"limit_id":"codex","primary":{"used_percent":25.0,"window_minutes":300,"resets_at":%d},"secondary":{"used_percent":5.0,"window_minutes":10080,"resets_at":%d},"plan_type":"plus"}}}
`, ts, ts, ts, now.Add(3*time.Hour).Unix(), now.Add(48*time.Hour).Unix())
	if err := os.WriteFile(filepath.Join(dir, "rollout-x-01a1857a-e4fd-7243-965a-464511471a77.jsonl"), []byte(rollout), 0o600); err != nil {
		t.Fatal(err)
	}

	root := NewRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"limits", "--json", "--agent", "codex"})
	if err := root.Execute(); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	var got struct {
		GeneratedAt time.Time `json:"generated_at"`
		Plans       []struct {
			Family  string   `json:"family"`
			Plan    string   `json:"plan"`
			Count   []string `json:"counted_fields"`
			Session *struct {
				Kind          string  `json:"kind"`
				Reported      bool    `json:"boundaries_reported"`
				CountedTokens int64   `json:"counted_tokens"`
				TotalTokens   int64   `json:"total_tokens"`
				RemainingSec  float64 `json:"remaining_seconds"`
				Burn          struct {
					Basis string `json:"basis"`
				} `json:"burn"`
				Limit struct {
					Source       string     `json:"source"`
					Estimated    bool       `json:"estimated"`
					Implied      bool       `json:"implied_from_percent"`
					UsedFraction float64    `json:"used_fraction"`
					Tokens       int64      `json:"tokens"`
					ObservedAt   *time.Time `json:"observed_at"`
				} `json:"limit"`
				ETASec *float64 `json:"eta_seconds"`
			} `json:"session"`
			Weekly struct {
				Kind  string `json:"kind"`
				Limit struct {
					Source       string  `json:"source"`
					UsedFraction float64 `json:"used_fraction"`
				} `json:"limit"`
			} `json:"weekly"`
			History []map[string]any `json:"history"`
		} `json:"plans"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out.String())
	}
	if len(got.Plans) != 1 {
		t.Fatalf("want only the openai family with --agent codex:\n%s", out.String())
	}
	p := got.Plans[0]
	s := p.Session
	if p.Family != "openai" || p.Plan != "plus" || len(p.Count) != 2 || s == nil {
		t.Fatalf("plan: %+v", p)
	}
	// input 1000 includes 600 cached: counted = 400 input + 100 output.
	if s.Kind != "session" || !s.Reported || s.CountedTokens != 500 || s.TotalTokens != 1100 || s.RemainingSec < 3*3600-120 {
		t.Fatalf("session: %+v", s)
	}
	if l := s.Limit; l.Source != "reported" || l.Estimated || !l.Implied || l.UsedFraction != 0.25 || l.Tokens != 2000 || l.ObservedAt == nil {
		t.Fatalf("limit: %+v", l)
	}
	if s.ETASec == nil {
		t.Fatal("eta_seconds should be set when a limit and burn rate are known")
	}
	if p.Weekly.Kind != "weekly" || p.Weekly.Limit.Source != "reported" || p.Weekly.Limit.UsedFraction != 0.05 {
		t.Fatalf("weekly: %+v", p.Weekly)
	}
	if len(p.History) != 1 || p.History[0]["active"] != true {
		t.Fatalf("history: %+v", p.History)
	}
}
