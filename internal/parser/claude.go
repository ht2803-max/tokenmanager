package parser

import (
	"encoding/json"
	"io"

	"github.com/ht2803-max/tokenmanager/internal/model"
)

// claudeLine is the subset of a Claude Code transcript line we read.
// Unknown fields are ignored so format additions do not break parsing.
type claudeLine struct {
	Type        string `json:"type"`
	UUID        string `json:"uuid"`
	Timestamp   string `json:"timestamp"`
	SessionID   string `json:"sessionId"`
	Cwd         string `json:"cwd"`
	RequestID   string `json:"requestId"`
	IsSidechain bool   `json:"isSidechain"`
	Message     *struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage *struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			CacheCreation            *struct {
				Ephemeral5m int64 `json:"ephemeral_5m_input_tokens"`
				Ephemeral1h int64 `json:"ephemeral_1h_input_tokens"`
			} `json:"cache_creation"`
			OutputTokensDetails *struct {
				ThinkingTokens int64 `json:"thinking_tokens"`
			} `json:"output_tokens_details"`
			Speed string `json:"speed"`
		} `json:"usage"`
	} `json:"message"`
}

// ParseClaude reads a Claude Code session transcript (~/.claude/projects/**/*.jsonl).
//
// Claude Code writes one line per content block of an assistant message, and
// every one of those lines repeats the same usage object. Records are keyed by
// message id + request id so storage-level dedup collapses them to one request.
// Resumed sessions that copy earlier history produce the same keys and are
// collapsed too.
func ParseClaude(r io.Reader, path string, st State) ([]model.Record, int64, error) {
	var out []model.Record
	n, err := ReadLines(r, func(line []byte) {
		var l claudeLine
		if json.Unmarshal(line, &l) != nil {
			return // tolerate corrupt or unknown lines
		}
		if l.Cwd != "" {
			st["cwd"] = l.Cwd
		}
		if l.Type != "assistant" || l.Message == nil || l.Message.Usage == nil {
			return
		}
		m := l.Message
		if m.Model == "" || m.Model == "<synthetic>" {
			return // locally generated messages, not API calls
		}
		u := m.Usage
		ts, ok := ParseTime(l.Timestamp)
		if !ok {
			return
		}
		rec := model.Record{
			Agent:      "claude-code",
			Provider:   "anthropic",
			Model:      m.Model,
			SessionID:  l.SessionID,
			Project:    firstNonEmpty(l.Cwd, st["cwd"]),
			Timestamp:  ts,
			Input:      u.InputTokens,
			Output:     u.OutputTokens,
			CacheRead:  u.CacheReadInputTokens,
			Speed:      u.Speed,
			Accuracy:   model.Actual,
			SourceFile: path,
		}
		if u.CacheCreation != nil && u.CacheCreation.Ephemeral5m+u.CacheCreation.Ephemeral1h == u.CacheCreationInputTokens {
			rec.CacheWrite5m = u.CacheCreation.Ephemeral5m
			rec.CacheWrite1h = u.CacheCreation.Ephemeral1h
		} else {
			rec.CacheWrite5m = u.CacheCreationInputTokens // TTL unknown: price as 5m
		}
		if u.OutputTokensDetails != nil {
			rec.Reasoning = u.OutputTokensDetails.ThinkingTokens
		}
		if rec.Total() == 0 {
			return
		}
		id := m.ID
		if id == "" {
			id = l.UUID
		}
		rec.Key = "claude:" + id + ":" + l.RequestID
		out = append(out, rec)
	})
	return out, n, err
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}
