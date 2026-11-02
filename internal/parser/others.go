package parser

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ht2803-max/tokenmanager/internal/model"
)

// ---------------------------------------------------------------- Gemini CLI

type geminiTokens struct {
	Input    int64 `json:"input"`
	Output   int64 `json:"output"`
	Cached   int64 `json:"cached"`
	Thoughts int64 `json:"thoughts"`
	Tool     int64 `json:"tool"`
}

type geminiSession struct {
	SessionID   string `json:"sessionId"`
	ProjectHash string `json:"projectHash"`
	Messages    []struct {
		ID        string        `json:"id"`
		Timestamp string        `json:"timestamp"`
		Type      string        `json:"type"`
		Model     string        `json:"model"`
		Tokens    *geminiTokens `json:"tokens"`
		ToolCalls []struct {
			ID     string          `json:"id"`
			Name   string          `json:"name"`
			Args   json.RawMessage `json:"args"`
			Result json.RawMessage `json:"result"`
		} `json:"toolCalls"`
	} `json:"messages"`
}

// ParseGemini reads a Gemini CLI chat file (~/.gemini/tmp/<project>/chats/session-*.json).
// The file is rewritten as a whole, so it is always parsed in full; message
// ids keep the records stable across re-reads. Gemini reports cached tokens
// inside the prompt count, and thoughts separately from candidates.
func ParseGemini(r io.Reader, path string, st State) ([]model.Record, int64, error) {
	recs, _, n, err := ParseGeminiTools(r, path, st)
	return recs, n, err
}

// ParseGeminiTools is ParseGemini plus tool calls. Gemini CLI records each
// call (id, name, args, result) on the gemini message that requested it, so
// call and result arrive together and link to that message's record.
func ParseGeminiTools(r io.Reader, path string, st State) ([]model.Record, []model.ToolCall, int64, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, nil, 0, err
	}
	var s geminiSession
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, nil, 0, fmt.Errorf("gemini session %s: %w", filepath.Base(path), err)
	}
	var tools []model.ToolCall
	project := st["project"]
	if project == "" && s.ProjectHash != "" {
		project = "gemini:" + s.ProjectHash[:min(8, len(s.ProjectHash))]
	}
	var out []model.Record
	for i, m := range s.Messages {
		if m.Tokens == nil || m.Type != "gemini" {
			continue
		}
		ts, ok := ParseTime(m.Timestamp)
		if !ok {
			continue
		}
		cached := min(m.Tokens.Cached, m.Tokens.Input)
		rec := model.Record{
			Agent:      "gemini-cli",
			Provider:   "google",
			Model:      firstNonEmpty(m.Model, "unknown"),
			SessionID:  s.SessionID,
			Project:    project,
			Timestamp:  ts,
			Input:      m.Tokens.Input - cached + m.Tokens.Tool,
			Output:     m.Tokens.Output + m.Tokens.Thoughts,
			CacheRead:  cached,
			Reasoning:  m.Tokens.Thoughts,
			Accuracy:   model.Actual,
			SourceFile: path,
		}
		if rec.Total() == 0 {
			continue
		}
		id := firstNonEmpty(m.ID, strconv.Itoa(i))
		rec.Key = "gemini:" + s.SessionID + ":" + id
		out = append(out, rec)
		for j, c := range m.ToolCalls {
			cid := "gemini:" + s.SessionID + ":" + firstNonEmpty(c.ID, id+"#"+strconv.Itoa(j))
			tc := toolCall(cid, c.Name, "gemini-cli", s.SessionID, rec.Key, ts, rawLen(c.Args))
			if len(c.Result) > 0 && string(c.Result) != "null" {
				res := toolResult(cid, "gemini-cli", s.SessionID, ts, c.Result)
				tc.ResultBytes, tc.ResultTokens, tc.HasResult = res.ResultBytes, res.ResultTokens, true
			}
			tools = append(tools, tc)
		}
	}
	return out, tools, int64(len(data)), nil
}

// ---------------------------------------------------------------- Aider

var (
	aiderSession = regexp.MustCompile(`^# aider chat started at (\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2})`)
	aiderModel   = regexp.MustCompile(`^> (?:Main model|Model): (\S+)`)
	aiderTokens  = regexp.MustCompile(`^> Tokens: (.+?)\. Cost: \$([\d.]+) message`)
	aiderPart    = regexp.MustCompile(`([\d.]+)([kKmM]?) (sent|received|cache write|cache hit)`)
)

// ParseAider reads `.aider.chat.history.md`. Aider prints rounded token
// counts ("12k sent, 1.2k received"), so records are marked Estimated, and
// the per-message cost Aider itself computed is kept as ReportedCost.
func ParseAider(r io.Reader, path string, st State) ([]model.Record, int64, error) {
	project := filepath.Dir(path)
	var out []model.Record
	n, err := ReadLines(r, func(line []byte) {
		s := string(line)
		if m := aiderSession.FindStringSubmatch(s); m != nil {
			st["session"] = m[1]
			st["seq"] = "0"
			return
		}
		if m := aiderModel.FindStringSubmatch(s); m != nil {
			st["model"] = m[1]
			return
		}
		m := aiderTokens.FindStringSubmatch(s)
		if m == nil || st["session"] == "" {
			return
		}
		start, err := time.ParseInLocation("2006-01-02 15:04:05", st["session"], time.Local)
		if err != nil {
			return
		}
		seq, _ := strconv.Atoi(st["seq"])
		seq++
		st["seq"] = strconv.Itoa(seq)
		rec := model.Record{
			Agent:      "aider",
			Provider:   providerForModel(st["model"]),
			Model:      firstNonEmpty(st["model"], "unknown"),
			SessionID:  "aider:" + project + "@" + st["session"],
			Project:    project,
			Timestamp:  start.Add(time.Duration(seq) * time.Second), // log has no per-message time
			Accuracy:   model.Estimated,
			SourceFile: path,
		}
		for _, p := range aiderPart.FindAllStringSubmatch(m[1], -1) {
			v := scaled(p[1], p[2])
			switch p[3] {
			case "sent":
				rec.Input = v
			case "received":
				rec.Output = v
			case "cache write":
				rec.CacheWrite5m = v
			case "cache hit":
				rec.CacheRead = v
			}
		}
		// Aider's "sent" includes cache hits and writes.
		rec.Input = max(0, rec.Input-rec.CacheRead-rec.CacheWrite5m)
		rec.ReportedCost, _ = strconv.ParseFloat(m[2], 64)
		rec.Key = fmt.Sprintf("aider:%s:%s:%d", project, st["session"], seq)
		out = append(out, rec)
	})
	return out, n, err
}

func scaled(num, unit string) int64 {
	f, _ := strconv.ParseFloat(num, 64)
	switch strings.ToLower(unit) {
	case "k":
		f *= 1e3
	case "m":
		f *= 1e6
	}
	return int64(f)
}

// ---------------------------------------------------------------- Continue

type continueLine struct {
	Model           string  `json:"model"`
	Provider        string  `json:"provider"`
	PromptTokens    int64   `json:"promptTokens"`
	GeneratedTokens int64   `json:"generatedTokens"`
	Timestamp       any     `json:"timestamp"`
	EventName       string  `json:"eventName"`
	SessionID       string  `json:"sessionId"`
	Cost            float64 `json:"cost"`
}

// ParseContinue reads Continue's dev data token log
// (~/.continue/dev_data/**/tokensGenerated.jsonl). Continue does not record
// cache usage or projects there.
func ParseContinue(r io.Reader, path string, st State) ([]model.Record, int64, error) {
	var out []model.Record
	offset, _ := strconv.ParseInt(st["offset"], 10, 64)
	n, err := ReadLines(r, func(line []byte) {
		offset++
		var l continueLine
		if json.Unmarshal(line, &l) != nil || l.PromptTokens+l.GeneratedTokens == 0 {
			return
		}
		var ts time.Time
		switch v := l.Timestamp.(type) {
		case string:
			ts, _ = ParseTime(v)
		case float64:
			ts = UnixAuto(v)
		}
		if ts.IsZero() {
			return
		}
		rec := model.Record{
			Agent:        "continue",
			Provider:     firstNonEmpty(l.Provider, providerForModel(l.Model)),
			Model:        firstNonEmpty(l.Model, "unknown"),
			SessionID:    firstNonEmpty(l.SessionID, "continue:"+ts.Local().Format("2006-01-02")),
			Timestamp:    ts,
			Input:        l.PromptTokens,
			Output:       l.GeneratedTokens,
			Accuracy:     model.Actual,
			ReportedCost: l.Cost,
			SourceFile:   path,
		}
		rec.Key = fmt.Sprintf("continue:%s:%d:%s", path, offset, ts.Format(time.RFC3339Nano))
		out = append(out, rec)
	})
	st["offset"] = strconv.FormatInt(offset, 10)
	return out, n, err
}

func providerForModel(m string) string {
	m = strings.ToLower(m)
	switch {
	case strings.Contains(m, "claude") || strings.HasPrefix(m, "anthropic/"):
		return "anthropic"
	case strings.Contains(m, "gemini"):
		return "google"
	case strings.Contains(m, "gpt") || strings.HasPrefix(m, "o1") || strings.HasPrefix(m, "o3") || strings.HasPrefix(m, "o4") || strings.HasPrefix(m, "openai/"):
		return "openai"
	case strings.Contains(m, "deepseek"):
		return "deepseek"
	case strings.Contains(m, "mistral") || strings.Contains(m, "codestral"):
		return "mistral"
	}
	return "unknown"
}
