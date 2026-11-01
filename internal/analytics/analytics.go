// Package analytics aggregates usage records. It is UI-independent so the
// CLI commands, the TUI and the daemon share one engine.
package analytics

import (
	"sort"
	"strings"
	"time"

	"github.com/tokenmanager/tokenmanager/internal/config"
	"github.com/tokenmanager/tokenmanager/internal/model"
	"github.com/tokenmanager/tokenmanager/internal/pricing"
	"github.com/tokenmanager/tokenmanager/internal/storage"
)

// Engine answers usage questions from the database.
type Engine struct {
	DB     *storage.DB
	Prices *pricing.Table
	Cfg    *config.Config
	Now    func() time.Time
}

// New builds an engine.
func New(cfg *config.Config, db *storage.DB) *Engine {
	return &Engine{DB: db, Prices: pricing.New(cfg), Cfg: cfg, Now: time.Now}
}

// Period is a named time range [From, To).
type Period struct {
	Name     string
	From, To time.Time
}

// Periods supported by --today/--week/--month and the TUI filter.
var PeriodNames = []string{"today", "week", "month", "30d", "all"}

// PeriodFor resolves a named period relative to now (local time, weeks start Monday).
func PeriodFor(name string, now time.Time) Period {
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	end := day.AddDate(0, 0, 1)
	switch name {
	case "today":
		return Period{"today", day, end}
	case "yesterday":
		return Period{"yesterday", day.AddDate(0, 0, -1), day}
	case "week":
		wd := (int(day.Weekday()) + 6) % 7
		return Period{"week", day.AddDate(0, 0, -wd), end}
	case "month":
		return Period{"month", time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()), end}
	case "30d":
		return Period{"30d", day.AddDate(0, 0, -29), end}
	case "7d":
		return Period{"7d", day.AddDate(0, 0, -6), end}
	}
	return Period{"all", time.Time{}, time.Time{}}
}

// Label is a human description of the period.
func (p Period) Label() string {
	switch p.Name {
	case "today":
		return "Today · " + p.From.Format("Mon Jan 2")
	case "week":
		return "This week · since " + p.From.Format("Mon Jan 2")
	case "month":
		return "This month · " + p.From.Format("January 2006")
	case "30d":
		return "Last 30 days"
	case "7d":
		return "Last 7 days"
	case "yesterday":
		return "Yesterday · " + p.From.Format("Mon Jan 2")
	}
	return "All time"
}

// Agg is an aggregate over a set of records.
type Agg struct {
	Key         string
	Requests    int64
	Input       int64
	Output      int64
	CacheRead   int64
	CacheWrite  int64
	Reasoning   int64
	Cost        float64
	Unpriced    int64 // tokens from models without a known price
	Estimated   int64 // requests whose token counts are estimates
	First, Last time.Time
	MaxPrompt   int64
	sessions    map[string]bool
	models      map[string]int64
}

// Tokens is the total billed tokens.
func (a *Agg) Tokens() int64 { return a.Input + a.Output + a.CacheRead + a.CacheWrite }

// Prompt is the total prompt-side tokens.
func (a *Agg) Prompt() int64 { return a.Input + a.CacheRead + a.CacheWrite }

// Sessions is the distinct session count.
func (a *Agg) Sessions() int { return len(a.sessions) }

// CacheHitRate is cache reads over all prompt tokens.
func (a *Agg) CacheHitRate() float64 {
	if p := a.Prompt(); p > 0 {
		return float64(a.CacheRead) / float64(p)
	}
	return 0
}

// TopModel is the model with the most tokens in the aggregate.
func (a *Agg) TopModel() string {
	best, bt := "", int64(-1)
	for m, t := range a.models {
		if t > bt || (t == bt && m < best) {
			best, bt = m, t
		}
	}
	return best
}

// Models lists models by token volume.
func (a *Agg) Models() []string {
	out := make([]string, 0, len(a.models))
	for m := range a.models {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return a.models[out[i]] > a.models[out[j]] })
	return out
}

// Add folds a record into the aggregate.
func (e *Engine) Add(a *Agg, r model.Record) {
	if a.sessions == nil {
		a.sessions, a.models = map[string]bool{}, map[string]int64{}
	}
	a.Requests++
	a.Input += r.Input
	a.Output += r.Output
	a.CacheRead += r.CacheRead
	a.CacheWrite += r.CacheWrite()
	a.Reasoning += r.Reasoning
	if c, ok := e.Prices.Cost(r); ok {
		a.Cost += c
	} else {
		a.Unpriced += r.Total()
	}
	if r.Accuracy == model.Estimated {
		a.Estimated++
	}
	if a.First.IsZero() || r.Timestamp.Before(a.First) {
		a.First = r.Timestamp
	}
	if r.Timestamp.After(a.Last) {
		a.Last = r.Timestamp
	}
	a.MaxPrompt = max(a.MaxPrompt, r.PromptTokens())
	a.sessions[r.Agent+"/"+r.SessionID] = true
	a.models[r.Model] += r.Total()
}

// Records loads records for a period and filter.
func (e *Engine) Records(p Period, f storage.Filter) ([]model.Record, error) {
	f.From, f.To = p.From, p.To
	return e.DB.Query(f)
}

// Summary aggregates all records.
func (e *Engine) Summary(recs []model.Record) Agg {
	var a Agg
	for _, r := range recs {
		e.Add(&a, r)
	}
	return a
}

// Dimension selects a grouping key.
type Dimension string

const (
	ByAgent    Dimension = "agent"
	ByModel    Dimension = "model"
	ByProject  Dimension = "project"
	ByProvider Dimension = "provider"
	BySession  Dimension = "session"
	ByDay      Dimension = "day"
)

func keyOf(d Dimension, r model.Record) string {
	switch d {
	case ByAgent:
		return r.Agent
	case ByModel:
		return r.Model
	case ByProject:
		if r.Project == "" {
			return "(unknown)"
		}
		return r.Project
	case ByProvider:
		return r.Provider
	case BySession:
		return r.Agent + "/" + r.SessionID
	case ByDay:
		return r.Timestamp.Local().Format("2006-01-02")
	}
	return ""
}

// Group aggregates records by a dimension, sorted by cost then tokens.
func (e *Engine) Group(recs []model.Record, d Dimension) []*Agg {
	m := map[string]*Agg{}
	for _, r := range recs {
		k := keyOf(d, r)
		a := m[k]
		if a == nil {
			a = &Agg{Key: k}
			m[k] = a
		}
		e.Add(a, r)
	}
	out := make([]*Agg, 0, len(m))
	for _, a := range m {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Cost != out[j].Cost {
			return out[i].Cost > out[j].Cost
		}
		if out[i].Tokens() != out[j].Tokens() {
			return out[i].Tokens() > out[j].Tokens()
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// Session is one agent session.
type Session struct {
	Agg
	ID      string
	Agent   string
	Project string
	// LastPrompt is the prompt size of the most recent request: a proxy for
	// how full the context window currently is.
	LastPrompt    int64
	ContextWindow int64
}

// Duration is wall time between first and last request.
func (s *Session) Duration() time.Duration { return s.Last.Sub(s.First) }

// Sessions groups records into sessions, most recent first.
func (e *Engine) Sessions(recs []model.Record) []*Session {
	m := map[string]*Session{}
	var order []*Session
	for _, r := range recs {
		k := r.Agent + "/" + r.SessionID
		s := m[k]
		if s == nil {
			s = &Session{ID: r.SessionID, Agent: r.Agent}
			s.Key = k
			m[k] = s
			order = append(order, s)
		}
		e.Add(&s.Agg, r)
		if r.Project != "" {
			s.Project = r.Project
		}
		if !r.Timestamp.Before(s.Last) {
			s.LastPrompt = r.PromptTokens()
			s.ContextWindow = r.ContextWindow
			if s.ContextWindow == 0 {
				s.ContextWindow = e.Prices.ContextWindow(r.Model)
			}
		}
	}
	sort.Slice(order, func(i, j int) bool { return order[i].Last.After(order[j].Last) })
	return order
}

// Point is one bucket of a time series.
type Point struct {
	Start  time.Time
	Tokens int64
	Input  int64
	Output int64
	Cost   float64
}

// Series buckets records into fixed steps between from and to.
func (e *Engine) Series(recs []model.Record, from, to time.Time, step time.Duration) []Point {
	if !to.After(from) || step <= 0 {
		return nil
	}
	n := int(to.Sub(from) / step)
	if to.Sub(from)%step != 0 {
		n++
	}
	pts := make([]Point, n)
	for i := range pts {
		pts[i].Start = from.Add(time.Duration(i) * step)
	}
	for _, r := range recs {
		if r.Timestamp.Before(from) || !r.Timestamp.Before(to) {
			continue
		}
		i := int(r.Timestamp.Sub(from) / step)
		pts[i].Tokens += r.Total()
		pts[i].Input += r.PromptTokens()
		pts[i].Output += r.Output
		if c, ok := e.Prices.Cost(r); ok {
			pts[i].Cost += c
		}
	}
	return pts
}

// DailySeries buckets by local calendar day (DST-safe).
func (e *Engine) DailySeries(recs []model.Record, days int, now time.Time) []Point {
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	pts := make([]Point, days)
	idx := map[string]int{}
	for i := range pts {
		d := today.AddDate(0, 0, i-days+1)
		pts[i].Start = d
		idx[d.Format("2006-01-02")] = i
	}
	for _, r := range recs {
		i, ok := idx[r.Timestamp.In(now.Location()).Format("2006-01-02")]
		if !ok {
			continue
		}
		pts[i].Tokens += r.Total()
		pts[i].Input += r.PromptTokens()
		pts[i].Output += r.Output
		if c, ok := e.Prices.Cost(r); ok {
			pts[i].Cost += c
		}
	}
	return pts
}

// Overview is the dashboard headline numbers.
type Overview struct {
	Today, Week, Month Agg
	ProjectedMonth     float64
	MonthBudget        float64
}

// Overview computes today/week/month totals and the month projection.
func (e *Engine) Overview(f storage.Filter) (Overview, []model.Record, error) {
	now := e.Now()
	month := PeriodFor("month", now)
	week := PeriodFor("week", now)
	from := month.From
	if week.From.Before(from) {
		from = week.From
	}
	recs, err := e.Records(Period{From: from, To: month.To}, f)
	if err != nil {
		return Overview{}, nil, err
	}
	today := PeriodFor("today", now)
	var o Overview
	for _, r := range recs {
		if !r.Timestamp.Before(month.From) {
			e.Add(&o.Month, r)
		}
		if !r.Timestamp.Before(week.From) {
			e.Add(&o.Week, r)
		}
		if !r.Timestamp.Before(today.From) {
			e.Add(&o.Today, r)
		}
	}
	o.ProjectedMonth = ProjectMonth(o.Month.Cost, now)
	o.MonthBudget = e.Cfg.Budgets.Global.Monthly
	return o, recs, nil
}

// ProjectMonth extrapolates month-to-date spend linearly over the month.
func ProjectMonth(spent float64, now time.Time) float64 {
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	end := start.AddDate(0, 1, 0)
	elapsed := now.Sub(start).Hours()
	if elapsed < 1 {
		elapsed = 1
	}
	return spent * end.Sub(start).Hours() / elapsed
}

// ShortProject shortens a project path for display.
func ShortProject(p string) string {
	if p == "" {
		return "(unknown)"
	}
	home := homeDir()
	if home != "" && strings.HasPrefix(p, home) {
		p = "~" + p[len(home):]
	}
	return p
}
