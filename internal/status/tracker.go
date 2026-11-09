package status

import "github.com/ht2803-max/tokenmanager/internal/model"

// Transition is a session changing state.
type Transition struct {
	From  model.State
	Event model.StatusEvent // the new observation (Event.State is the new state)
}

// Tracker remembers each session's last state and reports changes. It is fed
// full snapshots (Scanner.Scan); sessions missing from a snapshot have gone
// stale and are forgotten without a transition.
type Tracker struct {
	seeded   bool
	sessions map[string]model.StatusEvent
}

// NewTracker returns an empty tracker.
func NewTracker() *Tracker { return &Tracker{sessions: map[string]model.StatusEvent{}} }

// Update applies a snapshot and returns the state changes it contains. The
// first snapshot only seeds the tracker, so sessions that were already
// waiting when it started do not all fire at once. An observation older
// than the stored one (out of order) is ignored.
func (t *Tracker) Update(evs []model.StatusEvent) []Transition {
	var out []Transition
	present := make(map[string]bool, len(evs))
	for _, ev := range evs {
		k := key(ev)
		present[k] = true
		prev, ok := t.sessions[k]
		if ok && ev.At.Before(prev.At) {
			continue
		}
		t.sessions[k] = ev
		if t.seeded && (!ok || prev.State != ev.State) {
			from := model.State("")
			if ok {
				from = prev.State
			}
			out = append(out, Transition{From: from, Event: ev})
		}
	}
	for k := range t.sessions {
		if !present[k] {
			delete(t.sessions, k)
		}
	}
	t.seeded = true
	return out
}

// Sessions returns the current state of every tracked session.
func (t *Tracker) Sessions() []model.StatusEvent {
	out := make([]model.StatusEvent, 0, len(t.sessions))
	for _, ev := range t.sessions {
		out = append(out, ev)
	}
	Sort(out)
	return out
}
