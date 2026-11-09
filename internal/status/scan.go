// Package status builds the agent status board: which agent sessions are
// working, waiting for the user, done or idle. State is inferred from each
// agent's local session files by the adapters' probes; nothing is stored and
// no message content is read beyond what a probe needs to classify a line.
package status

import (
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/ht2803-max/tokenmanager/internal/agents"
	"github.com/ht2803-max/tokenmanager/internal/config"
	"github.com/ht2803-max/tokenmanager/internal/model"
	"github.com/ht2803-max/tokenmanager/internal/parser"
)

// wideTail is the second, larger read used when the normal tail holds no
// meaningful lines (for example a burst of large bookkeeping lines).
const wideTail = 1 << 20

type cached struct {
	mtime    time.Time
	size     int64
	probedAt time.Time
	ev       model.StatusEvent
	ok       bool
}

// Scanner probes session files. It caches each file's result and re-reads a
// file only when it changed or when enough time passed for a time-based
// state (waiting, idle, stale) to change, so frequent refreshes stay cheap.
type Scanner struct {
	cfg          *config.Config
	IncludeStale bool
	Now          func() time.Time
	cache        map[string]cached
}

// NewScanner returns a scanner for the configured agents.
func NewScanner(cfg *config.Config) *Scanner {
	return &Scanner{cfg: cfg, Now: time.Now, cache: map[string]cached{}}
}

// Options are the probe thresholds from the config.
func (s *Scanner) Options() model.ProbeOptions {
	st := s.cfg.Status
	return model.ProbeOptions{WaitingAfter: st.Waiting(), IdleAfter: st.Idle(), StaleAfter: st.Stale()}
}

// Scan returns one event per session, most urgent first.
func (s *Scanner) Scan() []model.StatusEvent {
	now := s.Now()
	o := s.Options()
	seen := map[string]bool{}
	merged := map[string]model.StatusEvent{}
	for _, a := range agents.All() {
		if a.Probe == nil {
			continue
		}
		d := agents.Discover(s.cfg, a)
		if !d.Enabled {
			continue
		}
		for _, path := range d.Files {
			info, err := os.Stat(path)
			if err != nil {
				continue
			}
			if !s.IncludeStale && now.Sub(info.ModTime()) >= o.StaleAfter {
				continue
			}
			seen[path] = true
			ev, ok := s.probe(a, path, info, now, o)
			if !ok {
				continue
			}
			if s.cfg.Privacy.HideProjectPaths && ev.Project != "" {
				ev.Project = filepath.Base(ev.Project)
			}
			k := key(ev)
			if prev, dup := merged[k]; !dup || newer(ev, prev) {
				merged[k] = ev
			}
		}
	}
	for p := range s.cache {
		if !seen[p] {
			delete(s.cache, p)
		}
	}
	out := make([]model.StatusEvent, 0, len(merged))
	for _, ev := range merged {
		if ev.State == model.StateStale && !s.IncludeStale {
			continue
		}
		out = append(out, ev)
	}
	Sort(out)
	return out
}

func (s *Scanner) probe(a *agents.Adapter, path string, info os.FileInfo, now time.Time, o model.ProbeOptions) (model.StatusEvent, bool) {
	c, hit := s.cache[path]
	if hit && c.mtime.Equal(info.ModTime()) && c.size == info.Size() && !crossed(o, c.mtime, c.probedAt, now) {
		return c.ev, c.ok
	}
	ev, ok := s.read(a, path, info, now, o, parser.TailSize)
	if !ok && info.Size() > parser.TailSize {
		ev, ok = s.read(a, path, info, now, o, wideTail)
	}
	s.cache[path] = cached{mtime: info.ModTime(), size: info.Size(), probedAt: now, ev: ev, ok: ok}
	return ev, ok
}

func (s *Scanner) read(a *agents.Adapter, path string, info os.FileInfo, now time.Time, o model.ProbeOptions, n int64) (model.StatusEvent, bool) {
	tail, err := parser.ReadTail(path, n)
	if err != nil {
		return model.StatusEvent{}, false
	}
	return a.Probe(path, tail, info.ModTime(), now, o)
}

// crossed reports whether a time threshold passed between two probes of an
// unchanged file, which can change its state without any new writes.
func crossed(o model.ProbeOptions, mtime, then, now time.Time) bool {
	a, b := then.Sub(mtime), now.Sub(mtime)
	for _, t := range []time.Duration{o.WaitingAfter, o.IdleAfter, o.StaleAfter} {
		if a < t && b >= t {
			return true
		}
	}
	return false
}

func key(ev model.StatusEvent) string { return ev.Agent + "\x00" + ev.SessionID }

// newer decides which of two observations of one session wins: the most
// recent activity, then the more urgent state.
func newer(a, b model.StatusEvent) bool {
	if !a.At.Equal(b.At) {
		return a.At.After(b.At)
	}
	return a.State.Rank() < b.State.Rank()
}

// Sort orders events most urgent first, then most recently active.
func Sort(evs []model.StatusEvent) {
	sort.SliceStable(evs, func(i, j int) bool {
		if ri, rj := evs[i].State.Rank(), evs[j].State.Rank(); ri != rj {
			return ri < rj
		}
		return evs[i].At.After(evs[j].At)
	})
}
