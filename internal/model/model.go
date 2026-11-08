// Package model defines the normalized usage schema shared by every adapter,
// the storage layer, analytics and the user interfaces.
package model

import (
	"strings"
	"time"
)

// Accuracy describes how trustworthy a metric is.
type Accuracy string

const (
	// Actual values were reported by the provider (e.g. API usage objects).
	Actual Accuracy = "actual"
	// Estimated values were derived (rounded logs, byte-length heuristics, ...).
	Estimated Accuracy = "estimated"
	// Unavailable means the agent does not expose the metric locally.
	Unavailable Accuracy = "unavailable"
)

// Record is one model request (one API call) normalized across agents.
//
// Token fields are disjoint: Input excludes cached tokens, so
// Input + CacheRead + CacheWrite5m + CacheWrite1h is the full prompt size.
type Record struct {
	Key           string    // globally unique dedup key (agent-specific)
	Agent         string    // adapter name, e.g. "claude-code"
	Provider      string    // "anthropic", "openai", "google", ...
	Model         string    // model id as reported by the agent
	SessionID     string    // agent session / thread id
	Project       string    // project path (cwd) or label when known
	Timestamp     time.Time // when the request completed
	Input         int64     // uncached input tokens
	Output        int64     // output tokens (includes reasoning/thinking)
	CacheRead     int64     // cache-hit input tokens
	CacheWrite5m  int64     // cache-write tokens, 5 minute TTL (or unknown TTL)
	CacheWrite1h  int64     // cache-write tokens, 1 hour TTL
	Reasoning     int64     // reasoning tokens, informational subset of Output
	ContextWindow int64     // model context window when the agent reports it
	Speed         string    // "fast" when a premium speed tier was used
	Accuracy      Accuracy  // accuracy of the token counts
	ReportedCost  float64   // cost reported by the agent itself (0 if none)
	SourceFile    string    // file the record was read from
}

// PromptTokens is the total prompt size sent to the model for this request.
func (r Record) PromptTokens() int64 {
	return r.Input + r.CacheRead + r.CacheWrite5m + r.CacheWrite1h
}

// CacheWrite is the total cache-write tokens across TTLs.
func (r Record) CacheWrite() int64 { return r.CacheWrite5m + r.CacheWrite1h }

// Total is every token billed for the request.
func (r Record) Total() int64 { return r.PromptTokens() + r.Output }

// RateLimitWindow is one provider-reported subscription usage window.
type RateLimitWindow struct {
	UsedPercent   float64   `json:"used_percent"`
	WindowMinutes int64     `json:"window_minutes"`
	ResetsAt      time.Time `json:"resets_at"`
}

// Current reports whether the window has not reset yet at now.
func (w *RateLimitWindow) Current(now time.Time) bool {
	return w != nil && w.WindowMinutes > 0 && now.Before(w.ResetsAt)
}

// Start is when the window began (ResetsAt minus its length).
func (w *RateLimitWindow) Start() time.Time {
	return w.ResetsAt.Add(-time.Duration(w.WindowMinutes) * time.Minute)
}

// RateLimits is the latest provider-reported snapshot of a subscription's
// usage windows (today only Codex writes one: 5 hour primary, weekly secondary).
type RateLimits struct {
	Provider  string           `json:"provider"`
	Agent     string           `json:"agent"`
	Plan      string           `json:"plan,omitempty"`
	Observed  time.Time        `json:"observed"`
	Primary   *RateLimitWindow `json:"primary,omitempty"`
	Secondary *RateLimitWindow `json:"secondary,omitempty"`
}

// ToolCall is one tool invocation (a tool_use block, a Codex function call,
// ...) and the size of its result. Only the tool name and byte counts are
// kept: inputs, outputs, paths, commands and URLs are measured, never stored.
//
// Parsers emit partial rows (the call, later its result, or a late link to
// the request) and storage merges them by ID, so a row may be incomplete
// until every part has been read.
type ToolCall struct {
	ID          string    // provider call id (tool_use.id, call_id): the merge key
	RecordKey   string    // Record.Key of the request that emitted the call
	SessionID   string    // agent session id
	Timestamp   time.Time // when the call (or, until it is seen, the result) was logged
	Agent       string    // adapter name
	Tool        string    // normalized tool name (MCP: the tool part only)
	MCPServer   string    // MCP server for mcp__<server>__<tool> names
	InputBytes  int64     // byte length of the call input (measured, not stored)
	ResultBytes int64     // byte length of the result text sent back to the model
	// ResultTokens estimates the result's prompt tokens: ~4 bytes per token
	// of text plus a fixed allowance per image.
	ResultTokens int64
	HasResult    bool
}

// ImageTokens is the per-image estimate used for image tool results, whose
// base64 size says nothing about their token cost.
const ImageTokens = 1600

// OtherTool groups calls whose name is empty or not a plain identifier.
const OtherTool = "other"

// NormalizeTool maps a raw tool name to (tool, mcp server). Built-in names
// are kept as they are; "mcp__<server>__<tool>" is split so usage can be
// grouped by server. Names that are empty, very long or contain anything but
// identifier characters become "other", so free text can never be stored.
func NormalizeTool(name string) (tool, server string) {
	if !plainName(name) {
		return OtherTool, ""
	}
	if rest, ok := strings.CutPrefix(name, "mcp__"); ok {
		srv, t, ok := strings.Cut(rest, "__")
		if !ok || srv == "" || t == "" {
			return OtherTool, ""
		}
		return t, srv
	}
	return name, ""
}

// ToolLabel is the display name of a normalized tool: "server/tool" for MCP.
func ToolLabel(tool, server string) string {
	if server != "" {
		return server + "/" + tool
	}
	return tool
}

func plainName(s string) bool {
	if s == "" || len(s) > 96 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-', c == '.', c == ':':
		default:
			return false
		}
	}
	return true
}
