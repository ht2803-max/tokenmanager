// Package collector ingests usage from agent files into storage, either as a
// one-shot incremental scan or continuously via filesystem events.
package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/ht2803-max/tokenmanager/internal/agents"
	"github.com/ht2803-max/tokenmanager/internal/config"
	"github.com/ht2803-max/tokenmanager/internal/model"
	"github.com/ht2803-max/tokenmanager/internal/parser"
	"github.com/ht2803-max/tokenmanager/internal/storage"
)

// Collector reads agent files into the database.
type Collector struct {
	cfg *config.Config
	db  *storage.DB
	log *slog.Logger
	mu  sync.Mutex // serializes ingestion
}

// New creates a collector.
func New(cfg *config.Config, db *storage.DB, log *slog.Logger) *Collector {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Collector{cfg: cfg, db: db, log: log}
}

// Result summarizes a scan.
type Result struct {
	Files   int
	Changed int
	Added   []model.Record
	Errors  []error
	// Reparsed counts previously read files re-read once from the start to
	// backfill data an older version did not collect (for example tool calls).
	Reparsed int
}

// Scan walks every enabled adapter's files and ingests new data. Files whose
// size and mtime are unchanged are skipped without being opened.
func (c *Collector) Scan() Result {
	var res Result
	// One-time re-read of files ingested before a parser learned new fields
	// (Codex rate_limits, tool calls). Dedup keys and tool-row merging keep
	// the re-read idempotent.
	reparse := map[string]bool{}
	for _, ro := range reparseOnce {
		if c.db.Get(ro.flag) == "" {
			reparse[ro.agent] = true
		}
	}
	for _, a := range agents.All() {
		if a.Parse == nil {
			continue
		}
		st := agents.Discover(c.cfg, a)
		if !st.Enabled {
			continue
		}
		for _, f := range st.Files {
			res.Files++
			seen := false
			if reparse[a.Name] {
				fs, _ := c.db.GetFile(f)
				seen = fs.Offset > 0 || fs.Size > 0
			}
			added, changed, err := c.ingest(a, f, reparse[a.Name])
			if err != nil {
				res.Errors = append(res.Errors, err)
				c.log.Warn("ingest failed", "file", f, "err", err)
			}
			if changed {
				res.Changed++
				if seen {
					res.Reparsed++
				}
			}
			res.Added = append(res.Added, added...)
		}
	}
	if len(res.Errors) == 0 {
		for _, ro := range reparseOnce {
			if reparse[ro.agent] {
				_ = c.db.Set(ro.flag, "1")
			}
		}
	}
	if c.cfg.RetentionDays > 0 {
		_, _ = c.db.Prune(time.Now().AddDate(0, 0, -c.cfg.RetentionDays))
	}
	return res
}

// reparseOnce lists one-time re-reads and the kv flag recording each. A
// re-read keeps every stored record (unlike "scan --rebuild", which would
// drop history whose transcripts the agent has since deleted) and only adds
// what the old parser skipped.
var reparseOnce = []struct{ agent, flag string }{
	{"codex", "reparsed:codex_rate_limits"},
	{"claude-code", "reparsed:tool_calls"},
	{"codex", "reparsed:tool_calls"},
	{"gemini-cli", "reparsed:tool_calls"},
}

func (c *Collector) ingest(a *agents.Adapter, path string, force bool) ([]model.Record, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	info, err := os.Stat(path)
	if err != nil {
		return nil, false, nil // vanished between discovery and read
	}
	fs, err := c.db.GetFile(path)
	if err != nil {
		return nil, false, err
	}
	size, mtime := info.Size(), info.ModTime().UnixNano()
	if fs.Size == size && fs.MTime == mtime && !force {
		return nil, false, nil
	}
	state := parser.State{}
	if fs.State != "" {
		_ = json.Unmarshal([]byte(fs.State), &state)
	}
	offset := fs.Offset
	if a.Mode == agents.Whole || size < offset || force {
		// Rewritten or truncated: start over. Dedup keys keep this idempotent.
		offset = 0
		if a.Mode == agents.Append {
			state = parser.State{}
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, false, err
	}
	var recs []model.Record
	var tools []model.ToolCall
	var n int64
	var perr error
	if a.ParseTools != nil {
		recs, tools, n, perr = a.ParseTools(f, path, state)
	} else {
		recs, n, perr = a.Parse(f, path, state)
	}
	if c.cfg.Privacy.HideProjectPaths {
		for i := range recs {
			if recs[i].Project != "" {
				recs[i].Project = filepath.Base(recs[i].Project)
			}
		}
	}
	added, err := c.db.Insert(recs)
	if err != nil {
		return nil, true, err
	}
	if err := c.db.UpsertTools(tools); err != nil {
		return added, true, err
	}
	if v := state[parser.RateLimitsKey]; v != "" {
		var rl model.RateLimits
		if json.Unmarshal([]byte(v), &rl) == nil {
			if err := c.db.SaveRateLimits(rl); err != nil {
				c.log.Warn("saving rate limits failed", "err", err)
			}
		}
	}
	if perr != nil {
		// Keep what was parsed but do not advance past an unreadable file.
		return added, true, fmt.Errorf("%s: %w", path, perr)
	}
	sb, _ := json.Marshal(state)
	if a.Mode == agents.Whole {
		offset = 0
		n = 0
	}
	err = c.db.PutFile(storage.FileState{Path: path, Agent: a.Name, Size: size, MTime: mtime, Offset: offset + n, State: string(sb)})
	return added, true, err
}

// Watch ingests continuously, sending newly added records on out. It uses
// filesystem events where available and a slow periodic rescan as a safety
// net for missed events (network filesystems, editors that replace files).
func (c *Collector) Watch(ctx context.Context, out chan<- []model.Record) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer w.Close()

	type root struct {
		dir string
		a   *agents.Adapter
	}
	var roots []root
	for _, a := range agents.All() {
		if a.Parse == nil {
			continue
		}
		st := agents.Discover(c.cfg, a)
		if !st.Enabled {
			continue
		}
		for _, r := range st.Roots {
			dir := r
			if info, err := os.Stat(r); err == nil && !info.IsDir() {
				dir = filepath.Dir(r)
				_ = w.Add(dir)
			} else {
				addTree(w, r)
			}
			roots = append(roots, root{r, a})
		}
	}
	adapterFor := func(p string) *agents.Adapter {
		for _, r := range roots {
			if (p == r.dir || strings.HasPrefix(p, r.dir+string(filepath.Separator))) && r.a.Match(p) {
				return r.a
			}
		}
		return nil
	}

	send := func(recs []model.Record) {
		if len(recs) == 0 {
			return
		}
		select {
		case out <- recs:
		case <-ctx.Done():
		}
	}

	pending := map[string]*agents.Adapter{}
	debounce := time.NewTimer(time.Hour)
	debounce.Stop()
	rescanEvery := max(c.cfg.RefreshInterval.Duration*12, 30*time.Second)
	rescan := time.NewTicker(rescanEvery)
	defer rescan.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-w.Events:
			if !ok {
				return nil
			}
			if ev.Op&fsnotify.Create != 0 {
				if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
					addTree(w, ev.Name)
					// Files may have been written before the watch was added.
					_ = filepath.WalkDir(ev.Name, func(p string, d os.DirEntry, err error) error {
						if err == nil && !d.IsDir() {
							if a := adapterFor(p); a != nil {
								pending[p] = a
							}
						}
						return nil
					})
				}
			}
			if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) != 0 {
				if a := adapterFor(ev.Name); a != nil {
					pending[ev.Name] = a
					debounce.Reset(250 * time.Millisecond)
				}
			}
		case err := <-w.Errors:
			c.log.Warn("watcher error", "err", err)
		case <-debounce.C:
			var batch []model.Record
			for p, a := range pending {
				added, _, err := c.ingest(a, p, false)
				if err != nil {
					c.log.Warn("ingest failed", "file", p, "err", err)
				}
				batch = append(batch, added...)
			}
			pending = map[string]*agents.Adapter{}
			send(batch)
		case <-rescan.C:
			send(c.Scan().Added)
		}
	}
}

func addTree(w *fsnotify.Watcher, root string) {
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			_ = w.Add(p)
		}
		return nil
	})
}
