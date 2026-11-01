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

	"github.com/ht2803-max/tokenmanager/internal/analytics"
	"github.com/ht2803-max/tokenmanager/internal/budget"
	"github.com/ht2803-max/tokenmanager/internal/collector"
	"github.com/ht2803-max/tokenmanager/internal/config"
	"github.com/ht2803-max/tokenmanager/internal/format"
	"github.com/ht2803-max/tokenmanager/internal/model"
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
