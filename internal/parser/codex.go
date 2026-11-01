package parser

import (
	"encoding/json"
	"io"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/tokenmanager/tokenmanager/internal/model"
)

type codexUsage struct {
	InputTokens           int64 `json:"input_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	CacheWriteInputTokens int64 `json:"cache_write_input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
	TotalTokens           int64 `json:"total_tokens"`
}

type codexLine struct {
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Payload   struct {
		Type          string `json:"type"`
		ID            string `json:"id"`
		SessionID     string `json:"session_id"`
		Cwd           string `json:"cwd"`
		Model         string `json:"model"`
		ModelProvider string `json:"model_provider"`
		Info          *struct {
			Total              *codexUsage `json:"total_token_usage"`
			Last               *codexUsage `json:"last_token_usage"`
			ModelContextWindow int64       `json:"model_context_window"`
		} `json:"info"`
	} `json:"payload"`
}

var codexFileID = regexp.MustCompile(`([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\.jsonl$`)

// ParseCodex reads an OpenAI Codex rollout file (~/.codex/sessions/**/rollout-*.jsonl).
//
// Usage comes from `event_msg` / `token_count` events: `last_token_usage` is
// the delta for one request and `total_token_usage` is the running total for
// the thread. Codex sometimes emits the same token_count twice, so records are
// keyed by the cumulative total, which only changes when a new request lands.
// The model is announced by `turn_context` lines and carried in State.
//
// OpenAI reports cached tokens as a subset of input tokens; they are split
// out here so Input never double counts the cache.
func ParseCodex(r io.Reader, path string, st State) ([]model.Record, int64, error) {
	if st["session"] == "" {
		if m := codexFileID.FindStringSubmatch(filepath.Base(path)); m != nil {
			st["session"] = m[1]
		}
	}
	var out []model.Record
	n, err := ReadLines(r, func(line []byte) {
		var l codexLine
		if json.Unmarshal(line, &l) != nil {
			return
		}
		p := l.Payload
		switch {
		case l.Type == "session_meta":
			st["session"] = firstNonEmpty(p.SessionID, p.ID, st["session"])
			if p.Cwd != "" {
				st["cwd"] = p.Cwd
			}
			if p.ModelProvider != "" {
				st["provider"] = p.ModelProvider
			}
		case l.Type == "turn_context":
			if p.Model != "" {
				st["model"] = p.Model
			}
			if p.Cwd != "" {
				st["cwd"] = p.Cwd
			}
		case l.Type == "event_msg" && p.Type == "token_count" && p.Info != nil && p.Info.Last != nil:
			ts, ok := ParseTime(l.Timestamp)
			if !ok {
				return
			}
			u := p.Info.Last
			// Cached reads and cache writes are both reported inside input_tokens.
			cached := min(u.CachedInputTokens, u.InputTokens)
			write := min(u.CacheWriteInputTokens, u.InputTokens-cached)
			rec := model.Record{
				Agent:         "codex",
				Provider:      firstNonEmpty(st["provider"], "openai"),
				Model:         firstNonEmpty(st["model"], "unknown"),
				SessionID:     st["session"],
				Project:       st["cwd"],
				Timestamp:     ts,
				Input:         u.InputTokens - cached - write,
				Output:        u.OutputTokens,
				CacheRead:     cached,
				CacheWrite5m:  write,
				Reasoning:     u.ReasoningOutputTokens,
				ContextWindow: p.Info.ModelContextWindow,
				Accuracy:      model.Actual,
				SourceFile:    path,
			}
			if rec.Total() == 0 {
				return
			}
			cum := l.Timestamp
			if p.Info.Total != nil {
				cum = strconv.FormatInt(p.Info.Total.TotalTokens, 10)
			}
			rec.Key = "codex:" + st["session"] + ":" + cum
			out = append(out, rec)
		}
	})
	return out, n, err
}
