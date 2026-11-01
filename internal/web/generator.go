// Package web exports the dashboard as a single standalone HTML file.
//
// All numbers are computed in Go by the shared analytics, pricing, budget and
// optimizer packages; the page only sums and draws the pre-aggregated values.
// The file holds aggregates, never prompts, responses or source code, and it
// loads nothing from the network.
package web

import (
	"math"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ht2803-max/tokenmanager/internal/agents"
	"github.com/ht2803-max/tokenmanager/internal/analytics"
	"github.com/ht2803-max/tokenmanager/internal/budget"
	"github.com/ht2803-max/tokenmanager/internal/format"
	"github.com/ht2803-max/tokenmanager/internal/model"
	"github.com/ht2803-max/tokenmanager/internal/optimizer"
	"github.com/ht2803-max/tokenmanager/internal/storage"
)

// DefaultSessionLimit caps how many recent sessions the page lists.
const DefaultSessionLimit = 1000

// Options controls what Build exports.
type Options struct {
	Filter       storage.Filter // --agent/--project/--model narrowing
	SessionLimit int            // 0 uses DefaultSessionLimit
	Scanned      bool           // whether ingestion ran before the export
	Version      string
}

// Dashboard is the data embedded in the page.
type Dashboard struct {
	Meta     Meta                `json:"meta"`
	Overview Overview            `json:"overview"`
	Dims     Dims                `json:"dims"`
	Facts    []Fact              `json:"facts"`
	Hourly   []Fact              `json:"hourly"`
	Agents   []AgentInfo         `json:"agents"`
	Models   []ModelInfo         `json:"models"`
	Sessions []Session           `json:"sessions"`
	Budgets  []BudgetStatus      `json:"budgets"`
	Findings []optimizer.Finding `json:"findings"`
}

// Meta describes the export and the calendar boundaries the page filters on.
// Dates are local "2006-01-02" strings so the page never does time zone math.
type Meta struct {
	GeneratedAt      time.Time `json:"generated_at"`
	Version          string    `json:"version"`
	Currency         string    `json:"currency"`
	Symbol           string    `json:"symbol"`
	Today            string    `json:"today"`
	WeekStart        string    `json:"week_start"`
	MonthStart       string    `json:"month_start"`
	Last7Start       string    `json:"last7_start"`
	Last30Start      string    `json:"last30_start"`
	MonthLabel       string    `json:"month_label"`
	ProjectionFactor float64   `json:"projection_factor"` // analytics.ProjectMonth(1, now)
	Records          int       `json:"records"`
	SessionsTotal    int       `json:"sessions_total"`
	SessionsShown    int       `json:"sessions_shown"`
	Scanned          bool      `json:"scanned"`
	HideProjectPaths bool      `json:"hide_project_paths"`
	Filter           string    `json:"filter,omitempty"`
	Thresholds       []float64 `json:"thresholds"`
}

// Totals is the serializable part of an analytics.Agg.
type Totals struct {
	Requests   int64   `json:"requests"`
	Input      int64   `json:"input"`
	Output     int64   `json:"output"`
	CacheRead  int64   `json:"cache_read"`
	CacheWrite int64   `json:"cache_write"`
	Tokens     int64   `json:"tokens"`
	Cost       float64 `json:"cost"`
	Unpriced   int64   `json:"unpriced"`
	Sessions   int     `json:"sessions"`
}

func totals(a *analytics.Agg) Totals {
	return Totals{a.Requests, a.Input, a.Output, a.CacheRead, a.CacheWrite, a.Tokens(), money(a.Cost), a.Unpriced, a.Sessions()}
}

// Overview is analytics.Engine.Overview for the unfiltered page state.
type Overview struct {
	Today          Totals  `json:"today"`
	Week           Totals  `json:"week"`
	Month          Totals  `json:"month"`
	ProjectedMonth float64 `json:"projected_month"`
	MonthBudget    float64 `json:"month_budget"`
}

// Dims are the dictionaries Fact and Session indices point into.
type Dims struct {
	Days     []string `json:"days"`
	Agents   []string `json:"agents"`
	Projects []string `json:"projects"`
	Models   []string `json:"models"`
}

// Fact is one (day or hour, agent, project, model) aggregate, encoded as an
// array to keep the page small:
//
//	[bucket, agent, project, model, requests, input, output, cache_read,
//	 cache_write, cost, unpriced_tokens, estimated_requests]
type Fact [12]float64

// AgentInfo is an adapter's detection status. Root paths are not exported.
type AgentInfo struct {
	Name         string `json:"name"`
	Display      string `json:"display"`
	Provider     string `json:"provider"`
	Enabled      bool   `json:"enabled"`
	Installed    bool   `json:"installed"`
	Files        int    `json:"files"`
	Availability string `json:"availability"`
	Note         string `json:"note"`
}

// ModelInfo carries list prices (USD per million tokens) for display only.
type ModelInfo struct {
	Name          string  `json:"name"`
	Provider      string  `json:"provider"`
	Priced        bool    `json:"priced"`
	Input         float64 `json:"input_price,omitempty"`
	Output        float64 `json:"output_price,omitempty"`
	CacheRead     float64 `json:"cache_read_price,omitempty"`
	ContextWindow int64   `json:"context_window,omitempty"`
}

// Session is one analytics.Session. Agent, Project and Models are indices
// into Dims; Models is ordered by token volume.
type Session struct {
	ID            string  `json:"id"`
	Agent         int     `json:"agent"`
	Project       int     `json:"project"`
	Models        []int   `json:"models"`
	Start         int64   `json:"start"` // unix ms
	End           int64   `json:"end"`
	Day           string  `json:"day"` // local day of the last request
	Requests      int64   `json:"requests"`
	Input         int64   `json:"input"`
	Output        int64   `json:"output"`
	CacheRead     int64   `json:"cache_read"`
	CacheWrite    int64   `json:"cache_write"`
	Cost          float64 `json:"cost"`
	Unpriced      int64   `json:"unpriced"`
	Estimated     int64   `json:"estimated"`
	LastPrompt    int64   `json:"last_prompt"`
	MaxPrompt     int64   `json:"max_prompt"`
	ContextWindow int64   `json:"context_window"`
}

// BudgetStatus is a budget.Status with its derived values precomputed.
type BudgetStatus struct {
	Name        string  `json:"name"`
	Scope       string  `json:"scope"`
	Target      string  `json:"target,omitempty"`
	Period      string  `json:"period"`
	Limit       float64 `json:"limit"`
	Spent       float64 `json:"spent"`
	Remaining   float64 `json:"remaining"`
	Fraction    float64 `json:"fraction"`
	Projected   float64 `json:"projected,omitempty"`
	PeriodStart string  `json:"period_start"`
	Level       float64 `json:"level"`
}

const dayFmt = "2006-01-02"

// Build queries the database through the shared engines and assembles the
// dashboard model. It reads aggregates only.
func Build(e *analytics.Engine, opt Options) (*Dashboard, error) {
	now := e.Now()
	loc := now.Location()
	if opt.SessionLimit <= 0 {
		opt.SessionLimit = DefaultSessionLimit
	}
	recs, err := e.Records(analytics.PeriodFor("all", now), opt.Filter)
	if err != nil {
		return nil, err
	}
	b := &builder{hide: e.Cfg.Privacy.HideProjectPaths, idx: map[string]map[string]int{}}
	d := &Dashboard{Facts: []Fact{}, Hourly: []Fact{}, Sessions: []Session{}, Budgets: []BudgetStatus{}, Findings: []optimizer.Finding{}}

	// Facts: fold records into (bucket, agent, project, model) aggregates
	// using the engine's own Add so costs come from the pricing table.
	today := now.Format(dayFmt)
	daily, hourly := map[factKey]*analytics.Agg{}, map[factKey]*analytics.Agg{}
	providers := map[string]string{}
	for _, r := range recs {
		day := r.Timestamp.In(loc).Format(dayFmt)
		k := factKey{b.dim("day", day), b.dim("agent", r.Agent), b.dim("project", b.path(r.Project)), b.dim("model", r.Model)}
		fold(e, daily, k, r)
		if day == today {
			hk := k
			hk.bucket = r.Timestamp.In(loc).Hour()
			fold(e, hourly, hk, r)
		}
		if _, ok := providers[r.Model]; !ok {
			providers[r.Model] = r.Provider
		}
	}
	toFacts := func(m map[factKey]*analytics.Agg) []Fact {
		out := make([]Fact, 0, len(m))
		for k, a := range m {
			out = append(out, Fact{float64(k.bucket), float64(k.agent), float64(k.project), float64(k.model),
				float64(a.Requests), float64(a.Input), float64(a.Output), float64(a.CacheRead), float64(a.CacheWrite),
				money(a.Cost), float64(a.Unpriced), float64(a.Estimated)})
		}
		sort.Slice(out, func(i, j int) bool {
			for c := 0; c < 4; c++ {
				if out[i][c] != out[j][c] {
					return out[i][c] < out[j][c]
				}
			}
			return false
		})
		return out
	}
	d.Facts, d.Hourly = toFacts(daily), toFacts(hourly)

	ss := e.Sessions(recs)
	d.Meta.SessionsTotal = len(ss)
	if len(ss) > opt.SessionLimit {
		ss = ss[:opt.SessionLimit]
	}
	for _, s := range ss {
		var ms []int
		for _, m := range s.Models() {
			ms = append(ms, b.dim("model", m))
		}
		d.Sessions = append(d.Sessions, Session{
			ID: s.ID, Agent: b.dim("agent", s.Agent), Project: b.dim("project", b.path(s.Project)), Models: ms,
			Start: s.First.UnixMilli(), End: s.Last.UnixMilli(), Day: s.Last.In(loc).Format(dayFmt),
			Requests: s.Requests, Input: s.Input, Output: s.Output, CacheRead: s.CacheRead, CacheWrite: s.CacheWrite,
			Cost: money(s.Cost), Unpriced: s.Unpriced, Estimated: s.Estimated,
			LastPrompt: s.LastPrompt, MaxPrompt: s.MaxPrompt, ContextWindow: s.ContextWindow,
		})
	}

	ov, _, err := e.Overview(opt.Filter)
	if err != nil {
		return nil, err
	}
	d.Overview = Overview{totals(&ov.Today), totals(&ov.Week), totals(&ov.Month), money(ov.ProjectedMonth), ov.MonthBudget}

	sts, err := budget.Evaluate(e)
	if err != nil {
		return nil, err
	}
	for _, s := range sts {
		bs := BudgetStatus{Name: s.Name(), Scope: s.Scope, Target: s.Target, Period: s.Period, Limit: s.Limit,
			Spent: money(s.Spent), Remaining: money(s.Remaining()), Fraction: s.Fraction(), Projected: money(s.Projected),
			PeriodStart: s.PeriodStart.Format(dayFmt), Level: s.Level(e.Cfg.Budgets.Thresholds)}
		if s.Scope == "project" {
			bs.Target = b.path(s.Target)
			bs.Name = strings.TrimSuffix(bs.Name, s.Target) + bs.Target
		}
		d.Budgets = append(d.Budgets, bs)
	}

	fs, err := optimizer.Analyze(e, opt.Filter)
	if err != nil {
		return nil, err
	}
	for _, f := range fs {
		f.Project, f.File = b.path(f.Project), b.path(f.File)
		if f.Project == "(unknown)" {
			f.Project = ""
		}
		if f.File == "(unknown)" {
			f.File = ""
		}
		f.MonthlySavings = money(f.MonthlySavings)
		d.Findings = append(d.Findings, f)
	}

	for _, a := range agents.All() {
		st := agents.Discover(e.Cfg, a)
		d.Agents = append(d.Agents, AgentInfo{a.Name, a.Display, a.Provider, st.Enabled, st.Installed, len(st.Files), string(a.Availability), a.Note})
	}
	for _, m := range b.dims["model"] {
		p, ok := e.Prices.Lookup(m)
		d.Models = append(d.Models, ModelInfo{m, providers[m], ok, p.Input, p.Output, p.CacheRead, p.ContextWindow})
	}

	d.Dims = Dims{Days: b.list("day"), Agents: b.list("agent"), Projects: b.list("project"), Models: b.list("model")}
	d.Meta = Meta{
		GeneratedAt: now, Version: opt.Version, Currency: format.Currency, Symbol: format.Symbol(),
		Today:            today,
		WeekStart:        analytics.PeriodFor("week", now).From.Format(dayFmt),
		MonthStart:       analytics.PeriodFor("month", now).From.Format(dayFmt),
		Last7Start:       analytics.PeriodFor("7d", now).From.Format(dayFmt),
		Last30Start:      analytics.PeriodFor("30d", now).From.Format(dayFmt),
		MonthLabel:       now.Format("January 2006"),
		ProjectionFactor: analytics.ProjectMonth(1, now),
		Records:          len(recs),
		SessionsTotal:    d.Meta.SessionsTotal,
		SessionsShown:    len(d.Sessions),
		Scanned:          opt.Scanned,
		HideProjectPaths: b.hide,
		Filter:           describe(opt.Filter),
		Thresholds:       e.Cfg.Budgets.Thresholds,
	}
	return d, nil
}

type factKey struct{ bucket, agent, project, model int }

func fold(e *analytics.Engine, m map[factKey]*analytics.Agg, k factKey, r model.Record) {
	a := m[k]
	if a == nil {
		a = &analytics.Agg{}
		m[k] = a
	}
	e.Add(a, r)
}

// builder interns dimension values and applies the path privacy rules.
type builder struct {
	hide bool
	idx  map[string]map[string]int
	dims map[string][]string
}

func (b *builder) dim(kind, v string) int {
	if b.idx[kind] == nil {
		b.idx[kind] = map[string]int{}
	}
	if i, ok := b.idx[kind][v]; ok {
		return i
	}
	if b.dims == nil {
		b.dims = map[string][]string{}
	}
	i := len(b.dims[kind])
	b.idx[kind][v] = i
	b.dims[kind] = append(b.dims[kind], v)
	return i
}

func (b *builder) list(kind string) []string {
	if l := b.dims[kind]; l != nil {
		return l
	}
	return []string{}
}

// path renders a project or file path for export: the last element when
// privacy.hide_project_paths is set, otherwise with the home directory as ~.
// Labels such as "(global)" pass through.
func (b *builder) path(p string) string {
	if p == "" || p == "(unknown)" {
		return "(unknown)"
	}
	if strings.HasPrefix(p, "(") {
		return p
	}
	if b.hide {
		return filepath.Base(filepath.Clean(p))
	}
	return analytics.ShortProject(p)
}

func describe(f storage.Filter) string {
	var parts []string
	if f.Agent != "" {
		parts = append(parts, "agent="+f.Agent)
	}
	if f.Project != "" {
		parts = append(parts, "project~"+f.Project)
	}
	if f.Model != "" {
		parts = append(parts, "model~"+f.Model)
	}
	return strings.Join(parts, " ")
}

// money rounds to 1e-6 so the page stays small without visible drift.
func money(v float64) float64 { return math.Round(v*1e6) / 1e6 }
