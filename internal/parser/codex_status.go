package parser

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ht2803-max/tokenmanager/internal/model"
)

// codexStatusLine is the subset of a rollout line the status probe reads.
type codexStatusLine struct {
	Type    string `json:"type"`
	Payload struct {
		Type   string `json:"type"`
		ID     string `json:"id"`
		Cwd    string `json:"cwd"`
		Name   string `json:"name"`
		CallID string `json:"call_id"`
	} `json:"payload"`
}

// ProbeCodex infers a Codex session's state from the tail of its rollout.
//
// Turns are bracketed by event_msg task_started / task_complete. Tool calls
// are response_item "*_call" entries matched to "*_call_output" by call_id;
// a call that stays unanswered with no further writes is probably waiting
// for approval.
func ProbeCodex(path string, tail []byte, mtime, now time.Time, o model.ProbeOptions) (model.StatusEvent, bool) {
	ev := model.StatusEvent{Agent: "codex", Source: "file", Confidence: model.High, At: mtime}
	var (
		last    lastKind
		pending pendingTools
		seen    bool
	)
	for _, line := range bytes.Split(tail, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var l codexStatusLine
		if json.Unmarshal(line, &l) != nil {
			continue
		}
		p := l.Payload
		switch l.Type {
		case "session_meta":
			ev.SessionID, ev.Project = pick(ev.SessionID, p.ID), pick(ev.Project, p.Cwd)
		case "turn_context":
			ev.Project = pick(ev.Project, p.Cwd)
		case "event_msg":
			switch p.Type {
			case "task_started", "user_message":
				last, seen = kindWorking, true
			case "task_complete", "turn_aborted":
				last, seen = kindDone, true
				pending.clear()
			}
		case "response_item":
			switch {
			case strings.HasSuffix(p.Type, "_call_output"):
				pending.resolve(p.CallID)
				last, seen = kindWorking, true
			case strings.HasSuffix(p.Type, "_call"):
				name := p.Name
				if name == "" {
					name = strings.TrimSuffix(p.Type, "_call")
				}
				pending.add(p.CallID, name)
				last, seen = kindTool, true
			case p.Type == "message" || p.Type == "reasoning":
				if last != kindDone {
					last, seen = kindWorking, true
				}
			}
		}
	}
	if !seen {
		return ev, false
	}
	if ev.SessionID == "" {
		if m := codexFileID.FindStringSubmatch(filepath.Base(path)); m != nil {
			ev.SessionID = m[1]
		}
	}
	if ev.Project == "" {
		ev.Project = codexHeadCwd(path)
	}
	ev.State, ev.Detail, ev.Confidence = classify(last, &pending, mtime, now, o)
	return ev, ev.SessionID != ""
}

// codexHeadCwd reads the cwd from the session_meta line at the start of a
// rollout, for sessions whose tail no longer includes it. The first line can
// be large (it embeds instructions), so it is decoded as a stream and only
// the needed fields are kept.
func codexHeadCwd(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	var l codexStatusLine
	if json.NewDecoder(bufio.NewReader(f)).Decode(&l) != nil || l.Type != "session_meta" {
		return ""
	}
	return l.Payload.Cwd
}
