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
		ID      string          `json:"id"`
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
		Usage   *struct {
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

// claudeBlock is the subset of a content block needed to size tool calls.
// Inputs and results are decoded only to be measured and are then dropped.
type claudeBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
}

func claudeBlocks(raw json.RawMessage) []claudeBlock {
	if len(raw) == 0 || raw[0] != '[' {
		return nil // plain string content
	}
	var bs []claudeBlock
	_ = json.Unmarshal(raw, &bs)
	return bs
}

// ParseClaude reads a Claude Code session transcript (~/.claude/projects/**/*.jsonl).
//
// Claude Code writes one line per content block of an assistant message, and
// every one of those lines repeats the same usage object. Records are keyed by
// message id + request id so storage-level dedup collapses them to one request.
// Resumed sessions that copy earlier history produce the same keys and are
// collapsed too.
func ParseClaude(r io.Reader, path string, st State) ([]model.Record, int64, error) {
	recs, _, n, err := ParseClaudeTools(r, path, st)
	return recs, n, err
}

// ParseClaudeTools is ParseClaude plus tool calls. A tool_use block on an
// assistant line becomes a call linked to that line's request key; a
// tool_result block on a later user line becomes the result half of the same
// row (linked by tool_use_id). Both are keyed by the tool_use id, so lines
// repeated by resumed or forked sessions merge instead of double counting,
// and a result read long after its call needs no parser state to find it.
func ParseClaudeTools(r io.Reader, path string, st State) ([]model.Record, []model.ToolCall, int64, error) {
	var out []model.Record
	var tools []model.ToolCall
	n, err := ReadLines(r, func(line []byte) {
		var l claudeLine
		if json.Unmarshal(line, &l) != nil {
			return // tolerate corrupt or unknown lines
		}
		if l.Cwd != "" {
			st["cwd"] = l.Cwd
		}
		if l.Type == "user" && l.Message != nil {
			ts, ok := ParseTime(l.Timestamp)
			if !ok {
				return
			}
			for _, b := range claudeBlocks(l.Message.Content) {
				if b.Type == "tool_result" && b.ToolUseID != "" {
					tools = append(tools, toolResult(b.ToolUseID, "claude-code", l.SessionID, ts, b.Content))
				}
			}
			return
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
		for _, b := range claudeBlocks(m.Content) {
			if b.Type == "tool_use" && b.ID != "" {
				tools = append(tools, toolCall(b.ID, b.Name, "claude-code", l.SessionID, rec.Key, ts, rawLen(b.Input)))
			}
		}
	})
	return out, tools, n, err
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}
