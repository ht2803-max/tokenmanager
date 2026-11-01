// Package model defines the normalized usage schema shared by every adapter,
// the storage layer, analytics and the user interfaces.
package model

import "time"

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
