package parser

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"

	"github.com/ht2803-max/tokenmanager/internal/model"
)

// claudeStatusLine is the subset of a transcript line the status probe reads.
type claudeStatusLine struct {
	Type        string `json:"type"`
	SessionID   string `json:"sessionId"`
	Cwd         string `json:"cwd"`
	IsSidechain bool   `json:"isSidechain"`
	IsMeta      bool   `json:"isMeta"`
	Message     *struct {
		StopReason *string         `json:"stop_reason"`
		Content    json.RawMessage `json:"content"`
	} `json:"message"`
}

// statusBlock is a content block reduced to its kind and ids. Text is decoded
// only to recognize Claude Code's own markers (interruptions, slash command
// output) and is never kept.
type statusBlock struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	ToolUseID string `json:"tool_use_id"`
	Text      string `json:"text"`
}

// lastKind is what the most recent meaningful line says about the turn.
type lastKind int

const (
	kindNone    lastKind = iota
	kindWorking          // prompt submitted, tool result returned, or output streaming
	kindTool             // assistant requested a tool
	kindDone             // assistant ended its turn (or the user interrupted)
)

// pendingTools tracks tool calls without a result, remembering the newest.
type pendingTools struct {
	names map[string]string
	order []string
}

func (p *pendingTools) add(id, name string) {
	if p.names == nil {
		p.names = map[string]string{}
	}
	if id == "" {
		return
	}
	if _, ok := p.names[id]; !ok {
		p.order = append(p.order, id)
	}
	p.names[id] = name
}

func (p *pendingTools) resolve(id string) { delete(p.names, id) }
func (p *pendingTools) clear()            { p.names = nil; p.order = nil }
func (p *pendingTools) len() int          { return len(p.names) }

// newest returns the most recently requested tool still pending.
func (p *pendingTools) newest() string {
	for i := len(p.order) - 1; i >= 0; i-- {
		if n, ok := p.names[p.order[i]]; ok {
			return n
		}
	}
	return ""
}

// ProbeClaude infers a Claude Code session's state from the tail of its
// transcript. Subagent transcripts (all lines isSidechain) report "working"
// for the parent session while they are being written.
func ProbeClaude(path string, tail []byte, mtime, now time.Time, o model.ProbeOptions) (model.StatusEvent, bool) {
	ev := model.StatusEvent{Agent: "claude-code", Source: "file", Confidence: model.High, At: mtime}
	var (
		last             lastKind
		pending          pendingTools
		sawMain, sawSide bool
		sideSID, sideCwd string
	)
	for _, line := range bytes.Split(tail, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var l claudeStatusLine
		if json.Unmarshal(line, &l) != nil || (l.Type != "user" && l.Type != "assistant") || l.Message == nil {
			continue
		}
		if l.IsSidechain {
			sawSide = true
			sideSID, sideCwd = pick(sideSID, l.SessionID), pick(sideCwd, l.Cwd)
			continue
		}
		if l.IsMeta {
			continue
		}
		sawMain = true
		ev.SessionID, ev.Project = pick(ev.SessionID, l.SessionID), pick(ev.Project, l.Cwd)
		str, blocks := statusContent(l.Message.Content)
		if l.Type == "assistant" {
			k := kindWorking
			for _, b := range blocks {
				if b.Type == "tool_use" {
					pending.add(b.ID, b.Name)
					k = kindTool
				}
			}
			if k != kindTool && l.Message.StopReason != nil {
				switch *l.Message.StopReason {
				case "end_turn", "stop_sequence", "max_tokens", "refusal":
					k = kindDone
					pending.clear()
				}
			}
			last = k
			continue
		}
		// user line: a tool result, a prompt, an interruption or command output
		k := kindWorking
		for _, b := range blocks {
			switch b.Type {
			case "tool_result":
				pending.resolve(b.ToolUseID)
			case "text":
				if claudeEndsTurn(b.Text) {
					k = kindDone
				}
			}
		}
		if claudeEndsTurn(str) {
			k = kindDone
		}
		if k == kindDone {
			pending.clear()
		}
		last = k
	}

	if !sawMain {
		if !sawSide {
			return ev, false
		}
		// A subagent transcript: it only says the parent is busy.
		ev.SessionID, ev.Project = sideSID, sideCwd
		ev.State, ev.Detail = model.StateWorking, "Subagent running"
		ev.State = o.Overlay(ev.State, mtime, now)
		if ev.State != model.StateWorking {
			return ev, false // finished subagents say nothing about the parent
		}
		return ev, ev.SessionID != ""
	}
	ev.State, ev.Detail, ev.Confidence = classify(last, &pending, mtime, now, o)
	return ev, ev.SessionID != ""
}

// classify turns the last line kind and pending tools into a state.
func classify(last lastKind, pending *pendingTools, mtime, now time.Time, o model.ProbeOptions) (model.State, string, model.Confidence) {
	st, detail, conf := model.StateWorking, "", model.High
	switch {
	case last == kindDone:
		st = model.StateDone
	case pending.len() > 0:
		tool := pending.newest()
		if now.Sub(mtime) >= o.WaitingAfter {
			st, detail, conf = model.StateWaiting, waitingDetail(tool), model.Low
		} else {
			detail = "Running " + tool
		}
	case last == kindNone:
		st = model.StateIdle
	}
	if s := o.Overlay(st, mtime, now); s != st {
		return s, "", model.High
	}
	return st, detail, conf
}

// waitingDetail describes what a session is probably waiting for.
func waitingDetail(tool string) string {
	switch tool {
	case "AskUserQuestion":
		return "Question"
	case "ExitPlanMode":
		return "Plan approval"
	case "":
		return "Permission needed"
	}
	return "Permission needed: " + tool
}

// claudeEndsTurn recognizes user-side lines Claude Code writes itself that
// leave the next move with the user: an interruption or slash command output.
func claudeEndsTurn(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasPrefix(s, "[Request interrupted") ||
		strings.HasPrefix(s, "<local-command-") ||
		strings.HasPrefix(s, "<command-name>")
}

func statusContent(raw json.RawMessage) (string, []statusBlock) {
	if len(raw) == 0 {
		return "", nil
	}
	if raw[0] == '"' {
		var s string
		_ = json.Unmarshal(raw, &s)
		if len(s) > 64 {
			s = s[:64] // only the prefix matters
		}
		return s, nil
	}
	var bs []statusBlock
	_ = json.Unmarshal(raw, &bs)
	return "", bs
}

func pick(cur, v string) string {
	if v != "" {
		return v
	}
	return cur
}
