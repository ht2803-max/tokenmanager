// Package tui is the interactive terminal dashboard (Bubble Tea).
package tui

import (
	"context"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/ht2803-max/tokenmanager/internal/agents"
	"github.com/ht2803-max/tokenmanager/internal/analytics"
	"github.com/ht2803-max/tokenmanager/internal/budget"
	"github.com/ht2803-max/tokenmanager/internal/collector"
	"github.com/ht2803-max/tokenmanager/internal/config"
	"github.com/ht2803-max/tokenmanager/internal/daemon"
	"github.com/ht2803-max/tokenmanager/internal/model"
	"github.com/ht2803-max/tokenmanager/internal/optimizer"
	"github.com/ht2803-max/tokenmanager/internal/storage"
)

// Views, in keyboard order 1-9 then 0.
const (
	vOverview = iota
	vUsage
	vAgents
	vSessions
	vProjects
	vModels
	vOptimize
	vBudgets
	vSettings
	vLimits
)

var viewNames = []string{"Overview", "Usage", "Agents", "Sessions", "Projects", "Models", "Optimize", "Budgets", "Settings", "Limits"}

// snapshot is everything the views render, computed off the UI goroutine.
type snapshot struct {
	period     analytics.Period
	recs       []model.Record
	ov         analytics.Overview
	daily      []analytics.Point // last 30 days
	series     []analytics.Point // period series (hourly for today)
	hourly     []analytics.Point // today, by hour
	byAgent    []*analytics.Agg
	byModel    []*analytics.Agg
	byProject  []*analytics.Agg
	byProvider []*analytics.Agg
	sessions   []*analytics.Session
	latest     []model.Record
	agentSt    []agents.Status
	budgets    []budget.Status
	limits     []analytics.PlanReport
	tools      analytics.ToolReport
	byTool     []*analytics.ToolStat
	findings   []optimizer.Finding
	optimized  bool
	daemonPID  int
	loadedAt   time.Time
	took       time.Duration
	err        error
}

type (
	dataMsg    *snapshot
	recordsMsg []model.Record
	tickMsg    time.Time
)

// Model is the Bubble Tea model.
type Model struct {
	cfg    *config.Config
	db     *storage.DB
	eng    *analytics.Engine
	coll   *collector.Collector
	filter storage.Filter

	w, h      int
	view      int
	periodIdx int
	data      *snapshot
	loading   bool
	dirty     bool // new data arrived while loading
	cursor    [10]int
	focus     int // panel focus within a view (Tab)
	detail    bool
	help      bool
	search    textinput.Model
	searching bool
	live      []model.Record // records seen by the watcher this run
	flash     time.Time      // last live update
	watchCh   chan []model.Record
	cancel    context.CancelFunc
}

// Run starts the dashboard.
func Run(cfg *config.Config, db *storage.DB, eng *analytics.Engine, coll *collector.Collector, f storage.Filter) error {
	ti := textinput.New()
	ti.Prompt = "/ "
	ti.Placeholder = "filter…"
	ti.CharLimit = 64
	ctx, cancel := context.WithCancel(context.Background())
	m := &Model{cfg: cfg, db: db, eng: eng, coll: coll, filter: f, search: ti, periodIdx: 2,
		watchCh: make(chan []model.Record, 16), cancel: cancel}
	go func() { _ = coll.Watch(ctx, m.watchCh) }()
	defer cancel()
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.load(), m.waitRecords(), tick(m.cfg.RefreshInterval.Duration))
}

func tick(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *Model) waitRecords() tea.Cmd {
	return func() tea.Msg { return recordsMsg(<-m.watchCh) }
}

func (m *Model) periodName() string { return analytics.PeriodNames[m.periodIdx] }

// load computes a snapshot in the background.
func (m *Model) load() tea.Cmd {
	m.loading = true
	eng, f, pname, view := m.eng, m.filter, m.periodName(), m.view
	withOpt := view == vOptimize || (m.data != nil && m.data.optimized)
	return func() tea.Msg {
		start := time.Now()
		s := &snapshot{loadedAt: start}
		now := eng.Now()
		s.period = analytics.PeriodFor(pname, now)
		var err error
		if s.ov, _, err = eng.Overview(f); err != nil {
			s.err = err
			return dataMsg(s)
		}
		recs30, err := eng.Records(analytics.PeriodFor("30d", now), f)
		if err != nil {
			s.err = err
			return dataMsg(s)
		}
		s.daily = eng.DailySeries(recs30, 30, now)
		today := analytics.PeriodFor("today", now)
		s.hourly = eng.Series(recs30, today.From, today.To, time.Hour)
		if pname == "30d" {
			s.recs = recs30
		} else if s.recs, err = eng.Records(s.period, f); err != nil {
			s.err = err
			return dataMsg(s)
		}
		switch pname {
		case "today":
			s.series = eng.Series(s.recs, s.period.From, s.period.From.Add(24*time.Hour), time.Hour)
		case "all":
			if len(s.recs) > 0 {
				first := s.recs[0].Timestamp
				days := int(now.Sub(first).Hours()/24) + 1
				s.series = eng.DailySeries(s.recs, min(days, 365), now)
			}
		default:
			days := int(now.Sub(s.period.From).Hours()/24) + 1
			s.series = eng.DailySeries(s.recs, days, now)
		}
		s.byAgent = eng.Group(s.recs, analytics.ByAgent)
		s.byModel = eng.Group(s.recs, analytics.ByModel)
		s.byProject = eng.Group(s.recs, analytics.ByProject)
		s.byProvider = eng.Group(s.recs, analytics.ByProvider)
		s.sessions = eng.Sessions(s.recs)
		if s.tools, err = eng.Tools(s.period, f, s.recs); err != nil {
			s.err = err
			return dataMsg(s)
		}
		s.byTool = s.tools.Group(analytics.ByTool)
		n := len(recs30)
		s.latest = append([]model.Record(nil), recs30[max(0, n-60):]...)
		for _, a := range agents.All() {
			s.agentSt = append(s.agentSt, agents.Discover(eng.Cfg, a))
		}
		s.budgets, _ = budget.Evaluate(eng)
		s.limits, _ = eng.Limits(f)
		if withOpt {
			s.findings, _ = optimizer.Analyze(eng, f)
			s.optimized = true
		}
		if pid, ok := daemon.Running(); ok {
			s.daemonPID = pid
		}
		s.took = time.Since(start)
		return dataMsg(s)
	}
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		return m, nil

	case dataMsg:
		m.data = msg
		m.loading = false
		m.clampCursor()
		if m.dirty {
			m.dirty = false
			return m, m.load()
		}
		return m, nil

	case recordsMsg:
		m.live = append(m.live, msg...)
		if len(m.live) > 200 {
			m.live = m.live[len(m.live)-200:]
		}
		m.flash = time.Now()
		cmds := []tea.Cmd{m.waitRecords()}
		if m.loading {
			m.dirty = true
		} else {
			cmds = append(cmds, m.load())
		}
		return m, tea.Batch(cmds...)

	case tickMsg:
		// Periodic refresh keeps relative times and day boundaries current.
		cmds := []tea.Cmd{tick(m.cfg.RefreshInterval.Duration * 6)}
		if !m.loading {
			cmds = append(cmds, m.load())
		}
		return m, tea.Batch(cmds...)

	case tea.KeyMsg:
		return m.key(msg)
	}
	return m, nil
}

func (m *Model) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.searching {
		switch k.String() {
		case "esc":
			m.searching = false
			m.search.SetValue("")
			m.search.Blur()
		case "enter":
			m.searching = false
			m.search.Blur()
		default:
			var cmd tea.Cmd
			m.search, cmd = m.search.Update(k)
			m.cursor[m.view] = 0
			return m, cmd
		}
		return m, nil
	}
	if m.help {
		m.help = false
		return m, nil
	}
	switch s := k.String(); s {
	case "q", "ctrl+c":
		m.cancel()
		return m, tea.Quit
	case "?":
		m.help = true
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		return m.switchView(int(s[0] - '1'))
	case "0":
		return m.switchView(vLimits)
	case "right", "l":
		return m.switchView((m.view + 1) % len(viewNames))
	case "left", "h":
		return m.switchView((m.view + len(viewNames) - 1) % len(viewNames))
	case "tab":
		if m.view == vOverview {
			m.focus = (m.focus + 1) % 2
			m.cursor[vOverview] = 0
		} else {
			m.detail = !m.detail
		}
	case "shift+tab":
		if m.view == vOverview {
			m.focus = (m.focus + 1) % 2
			m.cursor[vOverview] = 0
		}
	case "j", "down":
		m.move(1)
	case "k", "up":
		m.move(-1)
	case "pgdown", "ctrl+d":
		m.move(10)
	case "pgup", "ctrl+u":
		m.move(-10)
	case "g", "home":
		m.cursor[m.cursorView()] = 0
	case "G", "end":
		m.move(1 << 20)
	case "enter":
		if m.view == vOverview {
			if m.focus == 0 {
				m.cursor[vSessions] = m.cursor[vOverview]
				m.view, m.detail = vSessions, true
			} else {
				m.view = vAgents
			}
			return m, nil
		}
		m.detail = !m.detail
	case "esc":
		m.detail = false
		m.search.SetValue("")
	case "f":
		m.periodIdx = (m.periodIdx + 1) % len(analytics.PeriodNames)
		return m, m.load()
	case "F":
		m.periodIdx = (m.periodIdx + len(analytics.PeriodNames) - 1) % len(analytics.PeriodNames)
		return m, m.load()
	case "r":
		return m, m.rescan()
	case "/":
		m.searching = true
		m.search.Focus()
		return m, textinput.Blink
	}
	return m, nil
}

func (m *Model) switchView(v int) (tea.Model, tea.Cmd) {
	m.view, m.detail, m.focus = v, false, 0
	m.search.SetValue("")
	if v == vOptimize && (m.data == nil || !m.data.optimized) && !m.loading {
		return m, m.load()
	}
	return m, nil
}

// rescan ingests new files and reloads.
func (m *Model) rescan() tea.Cmd {
	m.loading = true
	coll := m.coll
	load := m.load()
	return func() tea.Msg {
		coll.Scan()
		return load()
	}
}

// cursorView maps the overview's session panel to its own cursor slot.
func (m *Model) cursorView() int { return m.view }

func (m *Model) move(d int) {
	n := m.listLen()
	if n == 0 {
		return
	}
	c := m.cursor[m.cursorView()] + d
	m.cursor[m.cursorView()] = max(0, min(c, n-1))
}

func (m *Model) clampCursor() {
	for v := range m.cursor {
		saved := m.view
		m.view = v
		if n := m.listLen(); m.cursor[v] >= n {
			m.cursor[v] = max(0, n-1)
		}
		m.view = saved
	}
}

// listLen is the number of selectable rows in the current view.
func (m *Model) listLen() int {
	if m.data == nil {
		return 0
	}
	switch m.view {
	case vOverview:
		if m.focus == 1 {
			return len(m.data.byAgent)
		}
		return min(len(m.data.sessions), 50)
	case vUsage:
		if m.detail {
			return len(m.filteredTools())
		}
	case vAgents:
		return len(m.data.agentSt)
	case vSessions:
		return len(m.filteredSessions())
	case vProjects:
		return len(m.filteredAggs(m.data.byProject))
	case vModels:
		return len(m.filteredAggs(m.data.byModel))
	case vOptimize:
		return len(m.data.findings)
	case vBudgets:
		return len(m.data.budgets)
	case vLimits:
		return len(m.limitRows())
	}
	return 0
}

func (m *Model) query() string { return strings.ToLower(strings.TrimSpace(m.search.Value())) }

func (m *Model) filteredSessions() []*analytics.Session {
	q := m.query()
	if q == "" {
		return m.data.sessions
	}
	var out []*analytics.Session
	for _, s := range m.data.sessions {
		if strings.Contains(strings.ToLower(s.Agent+" "+s.Project+" "+s.ID+" "+strings.Join(s.Models(), " ")), q) {
			out = append(out, s)
		}
	}
	return out
}

func (m *Model) filteredTools() []*analytics.ToolStat {
	q := m.query()
	if q == "" {
		return m.data.byTool
	}
	var out []*analytics.ToolStat
	for _, t := range m.data.byTool {
		if strings.Contains(strings.ToLower(t.Key), q) {
			out = append(out, t)
		}
	}
	return out
}

func (m *Model) filteredAggs(gs []*analytics.Agg) []*analytics.Agg {
	q := m.query()
	if q == "" {
		return gs
	}
	var out []*analytics.Agg
	for _, g := range gs {
		if strings.Contains(strings.ToLower(g.Key), q) {
			out = append(out, g)
		}
	}
	return out
}
