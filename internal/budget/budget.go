// Package budget evaluates spending against configured limits.
//
// Budgets are monitoring only: TokenManager cannot stop an agent or change a
// provider's own spend limits, it only reports and alerts.
package budget

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/tokenmanager/tokenmanager/internal/analytics"
	"github.com/tokenmanager/tokenmanager/internal/config"
	"github.com/tokenmanager/tokenmanager/internal/model"
	"github.com/tokenmanager/tokenmanager/internal/storage"
)

// Status is one budget's current state.
type Status struct {
	Scope       string // "global", "agent", "project", "model"
	Target      string // agent/project/model name ("" for global)
	Period      string // "daily", "weekly", "monthly"
	Limit       float64
	Spent       float64
	Projected   float64 // end-of-period projection (monthly only)
	PeriodStart time.Time
}

// Fraction is spent / limit.
func (s Status) Fraction() float64 {
	if s.Limit <= 0 {
		return 0
	}
	return s.Spent / s.Limit
}

// Remaining is limit - spent (may be negative).
func (s Status) Remaining() float64 { return s.Limit - s.Spent }

// Name is a display label.
func (s Status) Name() string {
	if s.Scope == "global" {
		return strings.ToUpper(s.Period[:1]) + s.Period[1:]
	}
	return fmt.Sprintf("%s %s: %s", strings.ToUpper(s.Period[:1])+s.Period[1:], s.Scope, s.Target)
}

// ID is a stable identifier used for alert de-duplication.
func (s Status) ID() string {
	return s.Scope + "|" + s.Target + "|" + s.Period + "|" + s.PeriodStart.Format("2006-01-02")
}

// Level is the highest threshold crossed (0 if none).
func (s Status) Level(thresholds []float64) float64 {
	lvl := 0.0
	for _, t := range thresholds {
		if s.Fraction() >= t && t > lvl {
			lvl = t
		}
	}
	return lvl
}

var periodName = map[string]string{"daily": "today", "weekly": "week", "monthly": "month"}

// Evaluate computes every configured budget.
func Evaluate(e *analytics.Engine) ([]Status, error) {
	cfg := e.Cfg
	now := e.Now()
	month := analytics.PeriodFor("month", now)
	from := month.From
	if w := analytics.PeriodFor("week", now); w.From.Before(from) {
		from = w.From
	}
	recs, err := e.DB.Query(storage.Filter{From: from})
	if err != nil {
		return nil, err
	}
	var out []Status
	add := func(scope, target string, b config.Budget, match func(model.Record) bool) {
		for _, p := range []struct {
			name  string
			limit float64
		}{{"daily", b.Daily}, {"weekly", b.Weekly}, {"monthly", b.Monthly}} {
			if p.limit <= 0 {
				continue
			}
			per := analytics.PeriodFor(periodName[p.name], now)
			st := Status{Scope: scope, Target: target, Period: p.name, Limit: p.limit, PeriodStart: per.From}
			for _, r := range recs {
				if r.Timestamp.Before(per.From) || !match(r) {
					continue
				}
				if c, ok := e.Prices.Cost(r); ok {
					st.Spent += c
				}
			}
			if p.name == "monthly" {
				st.Projected = analytics.ProjectMonth(st.Spent, now)
			}
			out = append(out, st)
		}
	}
	add("global", "", cfg.Budgets.Global, func(model.Record) bool { return true })
	for _, k := range sortedKeys(cfg.Budgets.PerAgent) {
		add("agent", k, cfg.Budgets.PerAgent[k], func(r model.Record) bool { return r.Agent == k })
	}
	for _, k := range sortedKeys(cfg.Budgets.PerProject) {
		add("project", k, cfg.Budgets.PerProject[k], func(r model.Record) bool { return strings.Contains(r.Project, k) })
	}
	for _, k := range sortedKeys(cfg.Budgets.PerModel) {
		add("model", k, cfg.Budgets.PerModel[k], func(r model.Record) bool { return strings.Contains(r.Model, k) })
	}
	return out, nil
}

// Set updates one budget in the config (limit 0 removes it).
func Set(cfg *config.Config, scope, target, period string, limit float64) error {
	apply := func(b config.Budget) (config.Budget, error) {
		switch period {
		case "daily":
			b.Daily = limit
		case "weekly":
			b.Weekly = limit
		case "monthly":
			b.Monthly = limit
		default:
			return b, fmt.Errorf("unknown period %q (want daily, weekly or monthly)", period)
		}
		return b, nil
	}
	var m *map[string]config.Budget
	switch scope {
	case "global", "":
		b, err := apply(cfg.Budgets.Global)
		cfg.Budgets.Global = b
		return err
	case "agent":
		m = &cfg.Budgets.PerAgent
	case "project":
		m = &cfg.Budgets.PerProject
	case "model":
		m = &cfg.Budgets.PerModel
	default:
		return fmt.Errorf("unknown scope %q", scope)
	}
	if *m == nil {
		*m = map[string]config.Budget{}
	}
	b, err := apply((*m)[target])
	if err != nil {
		return err
	}
	if b == (config.Budget{}) {
		delete(*m, target)
	} else {
		(*m)[target] = b
	}
	return nil
}

func sortedKeys(m map[string]config.Budget) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
