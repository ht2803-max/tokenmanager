package parser

import (
	"encoding/json"
	"io"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ht2803-max/tokenmanager/internal/model"
)

type codexUsage struct {
	InputTokens           int64 `json:"input_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	CacheWriteInputTokens int64 `json:"cache_write_input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
	TotalTokens           int64 `json:"total_tokens"`
}

type codexWindow struct {
	UsedPercent   float64 `json:"used_percent"`
	WindowMinutes int64   `json:"window_minutes"`
	ResetsAt      float64 `json:"resets_at"`
}

type codexRateLimits struct {
	LimitID   string       `json:"limit_id"`
	Primary   *codexWindow `json:"primary"`
	Secondary *codexWindow `json:"secondary"`
	PlanType  string       `json:"plan_type"`
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
		RateLimits *codexRateLimits `json:"rate_limits"`
		// response_item tool calls and their outputs (measured, never kept)
		Name      string          `json:"name"`
		CallID    string          `json:"call_id"`
		Arguments json.RawMessage `json:"arguments"`
		Input     json.RawMessage `json:"input"`
		Action    json.RawMessage `json:"action"`
		Output    json.RawMessage `json:"output"`
	} `json:"payload"`
}

// RateLimitsKey is the State key holding the file's latest provider-reported
// rate-limit snapshot (a JSON model.RateLimits); the collector stores it.
const RateLimitsKey = "rate_limits"

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
//
// token_count events also carry `rate_limits` (Codex 0.154+): the ChatGPT
// plan's 5 hour (primary) and weekly (secondary) windows with used_percent
// and resets_at. The latest "codex" snapshot is kept in State; snapshots for
// other limit ids, or with both windows null, are ignored.
func ParseCodex(r io.Reader, path string, st State) ([]model.Record, int64, error) {
	recs, _, n, err := ParseCodexTools(r, path, st)
	return recs, n, err
}

// codexPending is the State key listing call ids seen since the last
// token_count: the request that emitted them is only known when its
// token_count arrives, which may be in a later read.
const codexPending = "tools_pending"

// maxPending caps the pending list so a file without token_count events
// cannot grow the saved state without bound.
const maxPending = 256

// ParseCodexTools is ParseCodex plus tool calls. A rollout logs a call
// (function_call, custom_tool_call, local_shell_call, web_search_call), then
// its *_output linked by call_id, and only then the token_count of the request
// that emitted the call. Calls are emitted when seen and linked to that
// request by a second, key-only row at its token_count; ids waiting for it
// are kept in State and dropped once linked.
func ParseCodexTools(r io.Reader, path string, st State) ([]model.Record, []model.ToolCall, int64, error) {
	if st["session"] == "" {
		if m := codexFileID.FindStringSubmatch(filepath.Base(path)); m != nil {
			st["session"] = m[1]
		}
	}
	var out []model.Record
	var tools []model.ToolCall
	pending := strings.Fields(st[codexPending])
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
		case l.Type == "response_item" && codexCallTypes[p.Type] != "":
			ts, ok := ParseTime(l.Timestamp)
			id := firstNonEmpty(p.CallID, p.ID)
			if !ok || id == "" {
				return
			}
			name := p.Name
			if name == "" {
				name = codexCallTypes[p.Type]
			} else if !strings.HasPrefix(name, "mcp__") && strings.Contains(name, "__") {
				name = "mcp__" + name // Codex qualifies MCP tools as <server>__<tool>
			}
			in := rawLen(p.Arguments) + rawLen(p.Input) + rawLen(p.Action)
			tools = append(tools, toolCall("codex:"+id, name, "codex", st["session"], "", ts, in))
			if len(pending) < maxPending {
				pending = append(pending, "codex:"+id)
			}
		case l.Type == "response_item" && strings.HasSuffix(p.Type, "_output") && p.CallID != "":
			ts, ok := ParseTime(l.Timestamp)
			if !ok {
				return
			}
			tools = append(tools, toolResult("codex:"+p.CallID, "codex", st["session"], ts, p.Output))
		case l.Type == "event_msg" && p.Type == "token_count":
			ts, ok := ParseTime(l.Timestamp)
			if !ok {
				return
			}
			codexSnapshot(p.RateLimits, ts, st)
			if p.Info == nil || p.Info.Last == nil {
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
			for _, id := range pending {
				tools = append(tools, model.ToolCall{ID: id, RecordKey: rec.Key, Agent: "codex"})
			}
			pending = pending[:0]
		}
	})
	if len(pending) > 0 {
		st[codexPending] = strings.Join(pending, " ")
	} else {
		delete(st, codexPending)
	}
	return out, tools, n, err
}

// codexCallTypes maps response_item call types to the tool name used when
// the item has none.
var codexCallTypes = map[string]string{
	"function_call":    "function",
	"custom_tool_call": "custom",
	"local_shell_call": "shell",
	"web_search_call":  "web_search",
	"tool_search_call": "tool_search",
}

func codexSnapshot(rl *codexRateLimits, ts time.Time, st State) {
	if rl == nil || (rl.LimitID != "" && rl.LimitID != "codex") || (rl.Primary == nil && rl.Secondary == nil) {
		return
	}
	win := func(w *codexWindow) *model.RateLimitWindow {
		if w == nil || w.WindowMinutes <= 0 {
			return nil
		}
		return &model.RateLimitWindow{UsedPercent: w.UsedPercent, WindowMinutes: w.WindowMinutes, ResetsAt: UnixAuto(w.ResetsAt)}
	}
	snap := model.RateLimits{Provider: firstNonEmpty(st["provider"], "openai"), Agent: "codex", Plan: rl.PlanType,
		Observed: ts, Primary: win(rl.Primary), Secondary: win(rl.Secondary)}
	if b, err := json.Marshal(snap); err == nil {
		st[RateLimitsKey] = string(b)
	}
}
