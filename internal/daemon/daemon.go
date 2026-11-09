// Package daemon runs the collector in the background so usage is captured
// (and budget alerts fire) while the TUI is closed.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/ht2803-max/tokenmanager/internal/agents"
	"github.com/ht2803-max/tokenmanager/internal/analytics"
	"github.com/ht2803-max/tokenmanager/internal/budget"
	"github.com/ht2803-max/tokenmanager/internal/collector"
	"github.com/ht2803-max/tokenmanager/internal/config"
	"github.com/ht2803-max/tokenmanager/internal/format"
	"github.com/ht2803-max/tokenmanager/internal/model"
	"github.com/ht2803-max/tokenmanager/internal/status"
	"github.com/ht2803-max/tokenmanager/internal/storage"
)

// PIDPath is the daemon pid file.
func PIDPath() string { return filepath.Join(config.DataDir(), "daemon.pid") }

// LogPath is the daemon log file.
func LogPath() string { return filepath.Join(config.DataDir(), "daemon.log") }

// Running reports the daemon pid if it is alive.
func Running() (int, bool) {
	b, err := os.ReadFile(PIDPath())
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, alive(pid)
}

// Start launches "tokenmanager daemon run" detached from the terminal.
func Start() (int, error) {
	if pid, ok := Running(); ok {
		return pid, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(config.DataDir(), 0o700); err != nil {
		return 0, err
	}
	logf, err := os.OpenFile(LogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return 0, err
	}
	defer logf.Close()
	cmd := exec.Command(exe, "daemon", "run")
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = detachAttr()
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	// Give it a moment to fail fast (bad config etc.).
	time.Sleep(300 * time.Millisecond)
	if !alive(pid) {
		return 0, fmt.Errorf("daemon exited immediately; see %s", LogPath())
	}
	return pid, nil
}

// Stop terminates the daemon.
func Stop() error {
	pid, ok := Running()
	if !ok {
		_ = os.Remove(PIDPath())
		return errors.New("daemon is not running")
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := terminate(p); err != nil {
		return err
	}
	for i := 0; i < 30 && alive(pid); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	_ = os.Remove(PIDPath())
	return nil
}

// Run collects continuously until ctx is cancelled.
func Run(ctx context.Context, cfg *config.Config) error {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if pid, ok := Running(); ok && pid != os.Getpid() {
		return fmt.Errorf("another daemon is running (pid %d)", pid)
	}
	if err := os.MkdirAll(config.DataDir(), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(PIDPath(), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		return err
	}
	defer os.Remove(PIDPath())

	db, err := storage.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer db.Close()
	coll := collector.New(cfg, db, log)
	eng := analytics.New(cfg, db)

	res := coll.Scan()
	log.Info("daemon started", "files", res.Files, "new_requests", len(res.Added))
	checkBudgets(eng, db, cfg, log)
	checkBlocks(eng, db, cfg, log)

	ch := make(chan []model.Record, 16)
	errc := make(chan error, 1)
	go func() { errc <- coll.Watch(ctx, ch) }()
	tick := time.NewTicker(5 * time.Minute)
	defer tick.Stop()
	sw := newStatusWatch(cfg)
	sw.check(log) // seeds: sessions already waiting at startup do not notify
	stick := time.NewTicker(statusInterval)
	defer stick.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Info("daemon stopping")
			return nil
		case err := <-errc:
			return err
		case recs := <-ch:
			log.Info("ingested", "requests", len(recs))
			checkBudgets(eng, db, cfg, log)
			checkBlocks(eng, db, cfg, log)
		case <-tick.C:
			checkBudgets(eng, db, cfg, log)
			checkBlocks(eng, db, cfg, log)
		case <-stick.C:
			sw.check(log)
		}
	}
}

// checkBudgets notifies once per threshold per budget period.
func checkBudgets(eng *analytics.Engine, db *storage.DB, cfg *config.Config, log *slog.Logger) {
	st, err := budget.Evaluate(eng)
	if err != nil {
		log.Warn("budget evaluation failed", "err", err)
		return
	}
	for _, s := range st {
		lvl := s.Level(cfg.Budgets.Thresholds)
		if lvl == 0 {
			continue
		}
		key := "alert:" + s.ID()
		prev, _ := strconv.ParseFloat(db.Get(key), 64)
		if lvl <= prev {
			continue
		}
		_ = db.Set(key, strconv.FormatFloat(lvl, 'f', 2, 64))
		msg := fmt.Sprintf("%s budget at %.0f%%: %s of %s", s.Name(), s.Fraction()*100, format.Money(s.Spent), format.Money(s.Limit))
		log.Warn("budget alert", "budget", s.Name(), "usage", s.Fraction())
		if cfg.Notifications.Enabled {
			if err := Notify("TokenManager", msg); err != nil {
				log.Warn("notification failed", "err", err)
			}
		}
	}
}

// checkBlocks notifies (when plans.alerts is set) once per threshold per
// subscription window, using budgets.thresholds. State is keyed by the
// window start, so each new block starts fresh.
func checkBlocks(eng *analytics.Engine, db *storage.DB, cfg *config.Config, log *slog.Logger) {
	if !cfg.Plans.Alerts {
		return
	}
	reps, err := eng.Limits(storage.Filter{})
	if err != nil {
		log.Warn("limits evaluation failed", "err", err)
		return
	}
	for _, r := range reps {
		ws := []*analytics.Window{r.Session, &r.Weekly}
		for _, w := range ws {
			if w == nil || w.Rolling || w.Limit.Source == analytics.SourceNone {
				continue
			}
			lvl := 0.0
			for _, t := range cfg.Budgets.Thresholds {
				if w.Limit.Fraction >= t && t > lvl {
					lvl = t
				}
			}
			if lvl == 0 {
				continue
			}
			key := "alert:block|" + r.Family + "|" + w.Kind + "|" + w.Start.UTC().Format(time.RFC3339)
			prev, _ := strconv.ParseFloat(db.Get(key), 64)
			if lvl <= prev {
				continue
			}
			_ = db.Set(key, strconv.FormatFloat(lvl, 'f', 2, 64))
			msg := fmt.Sprintf("%s %s window at %.0f%% (%s), resets in %s", r.Family, w.Kind, w.Limit.Fraction*100,
				w.Limit.Label(), format.Duration(w.Remaining(eng.Now())))
			log.Warn("window alert", "family", r.Family, "window", w.Kind, "usage", w.Limit.Fraction, "source", w.Limit.Source)
			if cfg.Notifications.Enabled {
				if err := Notify("TokenManager", msg); err != nil {
					log.Warn("notification failed", "err", err)
				}
			}
		}
	}
}

// statusInterval is how often the daemon re-checks agent session states.
const statusInterval = 3 * time.Second

// renotifyAfter suppresses a repeat notification for a session that flaps
// back into the same state shortly after one was sent.
const renotifyAfter = time.Minute

// statusWatch turns agent status transitions into desktop notifications.
type statusWatch struct {
	cfg    *config.Config
	scan   *status.Scanner
	track  *status.Tracker
	sent   map[string]time.Time // agent|session|state -> last notification
	notify func(title, msg string) error
	now    func() time.Time
}

func newStatusWatch(cfg *config.Config) *statusWatch {
	return &statusWatch{cfg: cfg, scan: status.NewScanner(cfg), track: status.NewTracker(),
		sent: map[string]time.Time{}, notify: Notify, now: time.Now}
}

// check scans sessions and sends at most one notification per state
// (waiting, done) per check, naming every session that just entered it.
func (w *statusWatch) check(log *slog.Logger) {
	trs := w.track.Update(w.scan.Scan())
	if !w.cfg.Notifications.Enabled {
		return
	}
	now := w.now()
	var waiting, done []model.StatusEvent
	for _, tr := range trs {
		ev := tr.Event
		if w.cfg.Status.Muted(ev.Agent, ev.Project) {
			continue
		}
		k := ev.Agent + "|" + ev.SessionID + "|" + string(ev.State)
		if t, ok := w.sent[k]; ok && now.Sub(t) < renotifyAfter {
			continue
		}
		switch {
		case ev.State == model.StateWaiting && w.cfg.Status.NotifiesWaiting():
			waiting = append(waiting, ev)
		case ev.State == model.StateDone && w.cfg.Status.NotifyDone && tr.From == model.StateWorking:
			done = append(done, ev)
		default:
			continue
		}
		w.sent[k] = now
	}
	for k, t := range w.sent {
		if now.Sub(t) > time.Hour {
			delete(w.sent, k)
		}
	}
	for _, n := range []struct {
		evs  []model.StatusEvent
		verb string
	}{{waiting, "waiting"}, {done, "done"}} {
		if len(n.evs) == 0 {
			continue
		}
		title, msg := StatusMessage(n.evs, n.verb)
		log.Info("status notification", "state", n.verb, "sessions", len(n.evs))
		if err := w.notify(title, msg); err != nil {
			log.Warn("notification failed", "err", err)
		}
	}
}

// StatusMessage builds the notification for sessions that entered one state.
// File-inferred waiting is a guess, so it says "probably".
func StatusMessage(evs []model.StatusEvent, verb string) (title, msg string) {
	label := func(ev model.StatusEvent) string {
		p := filepath.Base(ev.Project)
		if p == "." || p == "/" || p == "" {
			p = ev.Agent
		}
		return p
	}
	low := false
	for _, ev := range evs {
		low = low || ev.Confidence == model.Low
	}
	state := verb
	if verb == "waiting" {
		state = "waiting for you"
		if low {
			state = "probably waiting for you"
		}
	}
	if len(evs) == 1 {
		ev := evs[0]
		title = fmt.Sprintf("%s · %s", label(ev), agentDisplay(ev.Agent))
		msg = capitalize(state)
		if ev.Detail != "" {
			msg += ": " + ev.Detail
		}
		return title, msg
	}
	names := make([]string, 0, len(evs))
	for _, ev := range evs {
		names = append(names, label(ev))
	}
	return fmt.Sprintf("%d agents %s", len(evs), state), strings.Join(names, ", ")
}

func agentDisplay(name string) string {
	if a := agents.ByName(name); a != nil {
		return a.Display
	}
	return name
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// Notify shows a desktop notification using the platform's native tool.
func Notify(title, msg string) error {
	switch runtime.GOOS {
	case "darwin":
		script := fmt.Sprintf("display notification %s with title %s", applescriptQuote(msg), applescriptQuote(title))
		return exec.Command("osascript", "-e", script).Run()
	case "linux":
		return exec.Command("notify-send", "--app-name=TokenManager", title, msg).Run()
	}
	return nil // unsupported: the alert is still logged
}

func applescriptQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
