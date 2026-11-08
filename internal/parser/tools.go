package parser

import (
	"encoding/json"
	"io"
	"strings"
	"time"

	"github.com/ht2803-max/tokenmanager/internal/model"
)

// ToolParseFunc is a parser that also reports tool calls. Tool rows are
// partial (a call, a result, or a late link to its request) and are merged by
// call id in storage, so a call and its result may come from different reads.
type ToolParseFunc func(r io.Reader, path string, st State) ([]model.Record, []model.ToolCall, int64, error)

// toolCall builds the call half of a tool row.
func toolCall(id, name, agent, session, recordKey string, ts time.Time, input int64) model.ToolCall {
	tool, srv := model.NormalizeTool(name)
	return model.ToolCall{ID: id, RecordKey: recordKey, SessionID: session, Timestamp: ts, Agent: agent,
		Tool: tool, MCPServer: srv, InputBytes: input}
}

// toolResult builds the result half of a tool row from the raw result JSON.
func toolResult(id, agent, session string, ts time.Time, raw json.RawMessage) model.ToolCall {
	n, images := measure(raw)
	return model.ToolCall{ID: id, SessionID: session, Timestamp: ts, Agent: agent,
		ResultBytes: n, ResultTokens: n/4 + int64(images)*model.ImageTokens, HasResult: true}
}

// measure returns the byte length of the text in a tool result and the number
// of images in it. Strings count by length; ids, types and media types do not
// count, and images count separately because their base64 size says nothing
// about their token cost. Nothing is retained.
func measure(raw json.RawMessage) (bytes int64, images int) {
	if len(raw) == 0 {
		return 0, 0
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return int64(len(raw)), 0
	}
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			bytes += int64(len(x))
		case []any:
			for _, e := range x {
				walk(e)
			}
		case map[string]any:
			if isImage(x) {
				images++
				return
			}
			for k, e := range x {
				if !metaKeys[k] {
					walk(e)
				}
			}
		}
	}
	walk(v)
	return bytes, images
}

var metaKeys = map[string]bool{"type": true, "id": true, "tool_use_id": true, "call_id": true, "name": true,
	"mimeType": true, "mime_type": true, "media_type": true, "is_error": true, "status": true}

func isImage(m map[string]any) bool {
	if t, _ := m["type"].(string); t == "image" || t == "input_image" || t == "image_url" {
		return true
	}
	_, inline := m["inlineData"]
	return inline
}

// rawLen is the byte length of a JSON value used to weight output tokens
// between parallel calls: a string's text, anything else as encoded.
func rawLen(raw json.RawMessage) int64 {
	if len(raw) > 0 && raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return int64(len(s))
		}
	}
	if s := strings.TrimSpace(string(raw)); s == "" || s == "null" {
		return 0
	}
	return int64(len(raw))
}
