package model

import "time"

// State is the live state of an agent session on the status board.
type State string

const (
	StateWorking State = "working" // generating or running tools
	StateWaiting State = "waiting" // blocked on the user (permission prompt, question)
	StateDone    State = "done"    // finished its turn; the next move is the user's
	StateIdle    State = "idle"    // nothing has happened for a while
	StateStale   State = "stale"   // long inactive; hidden by default
)

// Rank orders states by urgency (lower is more urgent).
func (s State) Rank() int {
	switch s {
	case StateWaiting:
		return 0
	case StateWorking:
		return 1
	case StateDone:
		return 2
	case StateIdle:
		return 3
	}
	return 4
}

// Confidence says how sure a status inference is. File-based inference of
// "waiting" is a heuristic (a long-running tool looks the same), so it is low.
type Confidence string

const (
	High Confidence = "high"
	Low  Confidence = "low"
)

// StatusEvent is one observation of a session's state. It carries metadata
// only (state, tool name, timestamps, project path), never message content.
type StatusEvent struct {
	Agent      string     `json:"agent"`
	SessionID  string     `json:"session"`
	Project    string     `json:"project"`
	State      State      `json:"state"`
	Detail     string     `json:"detail,omitempty"`
	Source     string     `json:"source"` // "file" (hooks arrive in M2)
	Confidence Confidence `json:"confidence"`
	// At is the last time the session wrote anything (file mtime). It orders
	// observations: an older At never overrides a newer one.
	At time.Time `json:"last_activity"`
}

// ProbeOptions are the timing thresholds a status probe applies.
type ProbeOptions struct {
	// WaitingAfter is how long a tool call may sit without a result and
	// without any file write before the session counts as waiting.
	WaitingAfter time.Duration
	IdleAfter    time.Duration
	StaleAfter   time.Duration
}

// Overlay applies the time-based states shared by every probe: done and
// working sessions become idle after IdleAfter, and anything becomes stale
// after StaleAfter. A waiting session stays waiting until stale, since a
// prompt that nobody answered is still blocking.
func (o ProbeOptions) Overlay(s State, lastWrite, now time.Time) State {
	age := now.Sub(lastWrite)
	if o.StaleAfter > 0 && age >= o.StaleAfter {
		return StateStale
	}
	if s != StateWaiting && o.IdleAfter > 0 && age >= o.IdleAfter {
		return StateIdle
	}
	return s
}
