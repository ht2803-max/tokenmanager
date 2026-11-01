package cli

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/tokenmanager/tokenmanager/internal/agents"
	"github.com/tokenmanager/tokenmanager/internal/analytics"
	"github.com/tokenmanager/tokenmanager/internal/format"
	"github.com/tokenmanager/tokenmanager/internal/model"
)

// aggJSON is the machine-readable form of an aggregate.
type aggJSON struct {
	Key              string     `json:"key,omitempty"`
	Requests         int64      `json:"requests"`
	Sessions         int        `json:"sessions"`
	InputTokens      int64      `json:"input_tokens"`
	OutputTokens     int64      `json:"output_tokens"`
	CacheReadTokens  int64      `json:"cache_read_tokens"`
	CacheWriteTokens int64      `json:"cache_write_tokens"`
	TotalTokens      int64      `json:"total_tokens"`
	EstimatedCost    float64    `json:"estimated_cost"`
	Currency         string     `json:"currency"`
	UnpricedTokens   int64      `json:"unpriced_tokens"`
	EstimatedCounts  int64      `json:"requests_with_estimated_tokens"`
	CacheHitRate     float64    `json:"cache_hit_rate"`
	Models           []string   `json:"models,omitempty"`
	First            *time.Time `json:"first,omitempty"`
	Last             *time.Time `json:"last,omitempty"`
}

func toJSON(a *analytics.Agg) aggJSON {
	j := aggJSON{
		Key: a.Key, Requests: a.Requests, Sessions: a.Sessions(),
		InputTokens: a.Input, OutputTokens: a.Output, CacheReadTokens: a.CacheRead, CacheWriteTokens: a.CacheWrite,
		TotalTokens: a.Tokens(), EstimatedCost: round2(a.Cost), Currency: format.Currency,
		UnpricedTokens: a.Unpriced, EstimatedCounts: a.Estimated, CacheHitRate: round2(a.CacheHitRate()),
		Models: a.Models(),
	}
	if !a.First.IsZero() {
		f, l := a.First, a.Last
		j.First, j.Last = &f, &l
	}
	return j
}

func round2(f float64) float64 { return float64(int64(f*100+0.5)) / 100 }

func groupsJSON(gs []*analytics.Agg) []aggJSON {
	out := make([]aggJSON, 0, len(gs))
	for _, g := range gs {
		out = append(out, toJSON(g))
	}
	return out
}

// groupTable renders a grouped breakdown as text, CSV or JSON.
func groupTable(e *env, title string, p analytics.Period, gs []*analytics.Agg, label func(string) string, extra bool) error {
	if fl.json {
		return writeJSON(e.out, map[string]any{"period": p.Name, "from": nz(p.From), "to": nz(p.To), "groups": groupsJSON(gs)})
	}
	var total analytics.Agg
	for _, g := range gs {
		total.Cost += g.Cost
	}
	t := newTable(strings.ToUpper(title[:1])+title[1:], "Requests", "Sessions", "Input", "Output", "Cache R", "Cache W", "Total", "Cost", "Share")
	t.alignRight(1, 2, 3, 4, 5, 6, 7, 8, 9)
	if extra {
		t.headers = append(t.headers, "Last used")
		t.right = append(t.right, true)
	}
	for _, g := range gs {
		share := ""
		if total.Cost > 0 {
			share = format.Percent(g.Cost / total.Cost)
		}
		row := []string{label(g.Key), format.Thousands(g.Requests), fmt.Sprint(g.Sessions()), format.Tokens(g.Input), format.Tokens(g.Output),
			format.Tokens(g.CacheRead), format.Tokens(g.CacheWrite), format.Tokens(g.Tokens()), costCell(g.Cost, g.Unpriced), share}
		if extra {
			row = append(row, format.Ago(g.Last, time.Now()))
		}
		t.add(row...)
	}
	if fl.csv {
		return t.csv(e.out)
	}
	heading(e.out, title, p.Label())
	if len(gs) == 0 {
		fmt.Fprintln(e.out, sDim.Render("  No usage recorded for this period."))
		return nil
	}
	t.render(e.out)
	footnotes(e, gs)
	return nil
}

func footnotes(e *env, gs []*analytics.Agg) {
	var unpriced, est int64
	models := map[string]bool{}
	for _, g := range gs {
		unpriced += g.Unpriced
		est += g.Estimated
	}
	if unpriced > 0 {
		for _, g := range gs {
			for _, m := range g.Models() {
				if _, ok := e.eng.Prices.Lookup(m); !ok {
					models[m] = true
				}
			}
		}
		var ms []string
		for m := range models {
			ms = append(ms, m)
		}
		fmt.Fprintf(e.out, "\n  %s %s tokens have no known price (%s). Add them under \"pricing:\" in %s.\n",
			sWarn.Render("*"), format.Tokens(unpriced), strings.Join(ms, ", "), "config.yaml")
	}
	if est > 0 {
		fmt.Fprintf(e.out, "  %s %d requests have estimated token counts (agent logs rounded values).\n", sDim.Render("~"), est)
	}
	fmt.Fprintln(e.out, sDim.Render("  Costs are estimates at API list prices; subscription plans are billed differently."))
}

func nz(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// ---------------------------------------------------------------- today/week/month

func periodCmd(name, short string) *cobra.Command {
	return &cobra.Command{
		Use: name, Short: short, Args: cobra.NoArgs,
		RunE: run(func(e *env, _ []string) error {
			p := analytics.PeriodFor(name, e.eng.Now())
			recs, err := e.eng.Records(p, e.filter())
			if err != nil {
				return err
			}
			return periodReport(e, p, recs)
		}),
	}
}

func periodReport(e *env, p analytics.Period, recs []model.Record) error {
	sum := e.eng.Summary(recs)
	byAgent := e.eng.Group(recs, analytics.ByAgent)
	byModel := e.eng.Group(recs, analytics.ByModel)
	byProject := e.eng.Group(recs, analytics.ByProject)
	projected := 0.0
	if p.Name == "month" {
		projected = analytics.ProjectMonth(sum.Cost, e.eng.Now())
	}
	if fl.json {
		return writeJSON(e.out, map[string]any{
			"period": p.Name, "from": nz(p.From), "to": nz(p.To), "summary": toJSON(&sum),
			"projected_month_cost": round2(projected),
			"by_agent":             groupsJSON(byAgent), "by_model": groupsJSON(byModel), "by_project": groupsJSON(byProject),
		})
	}
	if fl.csv {
		return groupTable(e, "agent", p, byAgent, func(s string) string { return s }, false)
	}
	w := e.out
	heading(w, p.Name, p.Label())
	if sum.Requests == 0 {
		fmt.Fprintln(w, sDim.Render("  No usage recorded for this period."))
		return nil
	}
	fmt.Fprintf(w, "  %-10s %s  %s\n", "Tokens", sBold.Render(format.Tokens(sum.Tokens())),
		sDim.Render(fmt.Sprintf("input %s · output %s · cache read %s · cache write %s",
			format.Tokens(sum.Input), format.Tokens(sum.Output), format.Tokens(sum.CacheRead), format.Tokens(sum.CacheWrite))))
	fmt.Fprintf(w, "  %-10s %s  %s\n", "Cost", sBold.Render(costCell(sum.Cost, sum.Unpriced)), sDim.Render("estimated at API list prices"))
	fmt.Fprintf(w, "  %-10s %s requests · %s · cache hit %s\n", "Activity", format.Thousands(sum.Requests), plural(sum.Sessions(), "session"), format.Percent(sum.CacheHitRate()))
	if p.Name == "month" {
		line := fmt.Sprintf("  %-10s %s", "Projected", sBold.Render(format.Money(projected)))
		if b := e.cfg.Budgets.Global.Monthly; b > 0 {
			line += sDim.Render(fmt.Sprintf("  of %s budget", format.Money(b)))
		}
		fmt.Fprintln(w, line)
	}
	now := e.eng.Now()
	var spark string
	switch p.Name {
	case "today":
		pts := e.eng.Series(recs, p.From, p.From.Add(24*time.Hour), time.Hour)
		spark = sparkOf(pts) + sDim.Render("  00h → 23h")
	default:
		days := int(now.Sub(p.From).Hours()/24) + 1
		pts := e.eng.DailySeries(recs, max(days, 1), now)
		spark = sparkOf(pts) + sDim.Render(fmt.Sprintf("  %s → today", p.From.Format("Jan 2")))
	}
	fmt.Fprintf(w, "  %-10s %s\n", "Trend", sAccent.Render(spark))

	heading(w, "by agent", "")
	agentTable(byAgent).render(w)
	heading(w, "by model", "")
	agentTable(byModel).render(w)
	heading(w, "top projects", "")
	if len(byProject) > 5 {
		byProject = byProject[:5]
	}
	t := agentTable(byProject)
	for i := range t.rows {
		t.rows[i][0] = format.TruncateLeft(analytics.ShortProject(byProject[i].Key), 48)
	}
	t.render(w)
	footnotes(e, byModel)
	return nil
}

func sparkOf(pts []analytics.Point) string {
	v := make([]float64, len(pts))
	for i, p := range pts {
		v[i] = float64(p.Tokens)
	}
	return format.Sparkline(v)
}

func agentTable(gs []*analytics.Agg) *table {
	var total float64
	for _, g := range gs {
		total += g.Cost
	}
	t := newTable("Name", "Requests", "Tokens", "Cost", "Share", "")
	t.alignRight(1, 2, 3, 4)
	for _, g := range gs {
		f := 0.0
		if total > 0 {
			f = g.Cost / total
		}
		t.add(g.Key, format.Thousands(g.Requests), format.Tokens(g.Tokens()), costCell(g.Cost, g.Unpriced), format.Percent(f), sAccent.Render(format.Bar(f, 16)))
	}
	return t
}

// ---------------------------------------------------------------- agents

func agentsCmd() *cobra.Command {
	return &cobra.Command{
		Use: "agents", Short: "Display usage grouped by coding agent, plus detection status", Args: cobra.NoArgs,
		RunE: run(func(e *env, _ []string) error {
			p := e.period("month")
			recs, err := e.eng.Records(p, e.filter())
			if err != nil {
				return err
			}
			groups := map[string]*analytics.Agg{}
			for _, g := range e.eng.Group(recs, analytics.ByAgent) {
				groups[g.Key] = g
			}
			type row struct {
				Name         string   `json:"name"`
				Display      string   `json:"display"`
				Enabled      bool     `json:"enabled"`
				Installed    bool     `json:"installed"`
				Files        int      `json:"files"`
				Roots        []string `json:"roots"`
				Availability string   `json:"usage_data"`
				Note         string   `json:"note"`
				Usage        *aggJSON `json:"usage,omitempty"`
			}
			var rows []row
			for _, a := range agents.All() {
				st := agents.Discover(e.cfg, a)
				r := row{Name: a.Name, Display: a.Display, Enabled: st.Enabled, Installed: st.Installed, Files: len(st.Files), Roots: st.Roots, Availability: string(a.Availability), Note: a.Note}
				if g := groups[a.Name]; g != nil {
					j := toJSON(g)
					r.Usage = &j
				}
				rows = append(rows, r)
			}
			if fl.json {
				return writeJSON(e.out, map[string]any{"period": p.Name, "agents": rows})
			}
			t := newTable("Agent", "Status", "Data", "Files", "Requests", "Tokens", "Cost", "Top model", "Last active")
			t.alignRight(3, 4, 5, 6)
			for _, r := range rows {
				status := sDim.Render("not found")
				switch {
				case !r.Enabled:
					status = sDim.Render("disabled")
				case r.Installed:
					status = sGood.Render("detected")
				}
				data := map[string]string{"actual": sGood.Render("actual"), "estimated": sWarn.Render("estimated"), "unavailable": sDim.Render("unavailable")}[r.Availability]
				req, tok, cost, top, last := "—", "—", "—", "—", "—"
				if g := groups[r.Name]; g != nil {
					req, tok, cost, top, last = format.Thousands(g.Requests), format.Tokens(g.Tokens()), costCell(g.Cost, g.Unpriced), g.TopModel(), format.Ago(g.Last, time.Now())
				}
				t.add(r.Display, status, data, fmt.Sprint(r.Files), req, tok, cost, top, last)
			}
			if fl.csv {
				return t.csv(e.out)
			}
			heading(e.out, "agents", p.Label())
			t.render(e.out)
			fmt.Fprintln(e.out)
			for _, r := range rows {
				if r.Installed || r.Files > 0 {
					fmt.Fprintf(e.out, "  %s %s\n", sBold.Render(r.Display+":"), sDim.Render(r.Note))
				}
			}
			return nil
		}),
	}
}

// ---------------------------------------------------------------- sessions

func sessionsCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use: "sessions", Short: "Show recent coding sessions", Args: cobra.NoArgs,
		RunE: run(func(e *env, _ []string) error {
			p := e.period("30d")
			recs, err := e.eng.Records(p, e.filter())
			if err != nil {
				return err
			}
			ss := e.eng.Sessions(recs)
			if limit > 0 && len(ss) > limit {
				ss = ss[:limit]
			}
			if fl.json {
				type sj struct {
					ID            string  `json:"id"`
					Agent         string  `json:"agent"`
					Project       string  `json:"project"`
					DurationSec   float64 `json:"duration_seconds"`
					LastPrompt    int64   `json:"last_prompt_tokens"`
					ContextWindow int64   `json:"context_window,omitempty"`
					aggJSON
				}
				out := make([]sj, 0, len(ss))
				for _, s := range ss {
					out = append(out, sj{s.ID, s.Agent, s.Project, s.Duration().Seconds(), s.LastPrompt, s.ContextWindow, toJSON(&s.Agg)})
				}
				return writeJSON(e.out, map[string]any{"period": p.Name, "sessions": out})
			}
			t := newTable("Last active", "Agent", "Project", "Model", "Duration", "Requests", "Tokens", "Context", "Cost")
			t.alignRight(4, 5, 6, 7, 8)
			for _, s := range ss {
				ctx := format.Tokens(s.LastPrompt)
				if s.ContextWindow > 0 {
					ctx += " " + format.Percent(float64(s.LastPrompt)/float64(s.ContextWindow))
				}
				t.add(s.Last.Local().Format("Jan 02 15:04"), s.Agent, format.TruncateLeft(analytics.ShortProject(s.Project), 36), s.TopModel(),
					format.Duration(s.Duration()), format.Thousands(s.Requests), format.Tokens(s.Tokens()), ctx, costCell(s.Cost, s.Unpriced))
			}
			if fl.csv {
				return t.csv(e.out)
			}
			heading(e.out, "sessions", p.Label())
			if len(ss) == 0 {
				fmt.Fprintln(e.out, sDim.Render("  No sessions in this period."))
				return nil
			}
			t.render(e.out)
			return nil
		}),
	}
	cmd.Flags().IntVarP(&limit, "limit", "n", 25, "maximum sessions to show (0 = all)")
	return cmd
}

// ---------------------------------------------------------------- projects / models

func projectsCmd() *cobra.Command {
	return &cobra.Command{
		Use: "projects", Short: "Display project-level consumption", Args: cobra.NoArgs,
		RunE: run(func(e *env, _ []string) error {
			p := e.period("month")
			recs, err := e.eng.Records(p, e.filter())
			if err != nil {
				return err
			}
			return groupTable(e, "projects", p, e.eng.Group(recs, analytics.ByProject), func(s string) string {
				if fl.csv {
					return s
				}
				return format.TruncateLeft(analytics.ShortProject(s), 44)
			}, true)
		}),
	}
}

func modelsCmd() *cobra.Command {
	return &cobra.Command{
		Use: "models", Short: "Compare model usage and costs", Args: cobra.NoArgs,
		RunE: run(func(e *env, _ []string) error {
			p := e.period("month")
			recs, err := e.eng.Records(p, e.filter())
			if err != nil {
				return err
			}
			gs := e.eng.Group(recs, analytics.ByModel)
			type mj struct {
				aggJSON
				Priced           bool    `json:"priced"`
				InputPrice       float64 `json:"input_price_per_mtok,omitempty"`
				OutputPrice      float64 `json:"output_price_per_mtok,omitempty"`
				CostPerRequest   float64 `json:"cost_per_request"`
				OutputPerRequest int64   `json:"output_tokens_per_request"`
			}
			if fl.json {
				var out []mj
				for _, g := range gs {
					pr, ok := e.eng.Prices.Lookup(g.Key)
					out = append(out, mj{toJSON(g), ok, pr.Input, pr.Output, round2(g.Cost / float64(max(g.Requests, 1))), g.Output / max(g.Requests, 1)})
				}
				return writeJSON(e.out, map[string]any{"period": p.Name, "models": out})
			}
			t := newTable("Model", "Requests", "Tokens", "Cache hit", "Out/req", "Cost/req", "$/M in · out", "Cost", "Share")
			t.alignRight(1, 2, 3, 4, 5, 6, 7, 8)
			var total float64
			for _, g := range gs {
				total += g.Cost
			}
			for _, g := range gs {
				pr, ok := e.eng.Prices.Lookup(g.Key)
				price := sDim.Render("unpriced")
				if ok {
					price = fmt.Sprintf("%g · %g", pr.Input, pr.Output)
				}
				share := ""
				if total > 0 {
					share = format.Percent(g.Cost / total)
				}
				t.add(g.Key, format.Thousands(g.Requests), format.Tokens(g.Tokens()), format.Percent(g.CacheHitRate()),
					format.Tokens(g.Output/max(g.Requests, 1)), format.Money(g.Cost/float64(max(g.Requests, 1))), price, costCell(g.Cost, g.Unpriced), share)
			}
			if fl.csv {
				return t.csv(e.out)
			}
			heading(e.out, "models", p.Label())
			if len(gs) == 0 {
				fmt.Fprintln(e.out, sDim.Render("  No usage recorded for this period."))
				return nil
			}
			t.render(e.out)
			footnotes(e, gs)
			return nil
		}),
	}
}

// ---------------------------------------------------------------- costs

func costsCmd() *cobra.Command {
	return &cobra.Command{
		Use: "costs", Short: "Display detailed spending breakdowns", Args: cobra.NoArgs,
		RunE: run(func(e *env, _ []string) error {
			p := e.period("month")
			recs, err := e.eng.Records(p, e.filter())
			if err != nil {
				return err
			}
			// Split cost by token category for each model.
			type split struct {
				Model                                       string
				Input, Output, CacheRead, CacheWrite, Total float64
				Priced                                      bool
			}
			m := map[string]*split{}
			var order []string
			for _, r := range recs {
				s := m[r.Model]
				if s == nil {
					s = &split{Model: r.Model}
					m[r.Model] = s
					order = append(order, r.Model)
				}
				c, ok := e.eng.Prices.Cost(r)
				if !ok {
					continue
				}
				s.Priced = true
				s.Total += c
				part := func(rr model.Record) float64 { v, _ := e.eng.Prices.Cost(rr); return v }
				base := model.Record{Model: r.Model, Speed: r.Speed, ReportedCost: 0}
				if r.ReportedCost > 0 {
					// Agent-reported totals cannot be split by category.
					s.Input += c
					continue
				}
				in, out, cr, cw := base, base, base, base
				in.Input, out.Output, cr.CacheRead = r.Input, r.Output, r.CacheRead
				cw.CacheWrite5m, cw.CacheWrite1h = r.CacheWrite5m, r.CacheWrite1h
				// Long-context tiers depend on the full prompt size; scale parts to the true total.
				parts := []float64{part(in), part(out), part(cr), part(cw)}
				sum := parts[0] + parts[1] + parts[2] + parts[3]
				if sum > 0 {
					k := c / sum
					s.Input += parts[0] * k
					s.Output += parts[1] * k
					s.CacheRead += parts[2] * k
					s.CacheWrite += parts[3] * k
				}
			}
			sort.SliceStable(order, func(i, j int) bool { return m[order[i]].Total > m[order[j]].Total })
			byProvider := e.eng.Group(recs, analytics.ByProvider)
			byAgent := e.eng.Group(recs, analytics.ByAgent)
			if fl.json {
				var rows []map[string]any
				for _, k := range order {
					s := m[k]
					rows = append(rows, map[string]any{"model": k, "priced": s.Priced, "input_cost": round2(s.Input), "output_cost": round2(s.Output),
						"cache_read_cost": round2(s.CacheRead), "cache_write_cost": round2(s.CacheWrite), "total_cost": round2(s.Total)})
				}
				sum := e.eng.Summary(recs)
				return writeJSON(e.out, map[string]any{"period": p.Name, "currency": format.Currency, "total": round2(sum.Cost),
					"projected_month_cost": round2(analytics.ProjectMonth(e.eng.Summary(mustMonth(e)).Cost, e.eng.Now())),
					"by_model":             rows, "by_provider": groupsJSON(byProvider), "by_agent": groupsJSON(byAgent)})
			}
			t := newTable("Model", "Input", "Output", "Cache read", "Cache write", "Total")
			t.alignRight(1, 2, 3, 4, 5)
			var tot split
			for _, k := range order {
				s := m[k]
				if !s.Priced {
					t.add(k, "—", "—", "—", "—", sDim.Render("unpriced"))
					continue
				}
				t.add(k, format.Money(s.Input), format.Money(s.Output), format.Money(s.CacheRead), format.Money(s.CacheWrite), sBold.Render(format.Money(s.Total)))
				tot.Input += s.Input
				tot.Output += s.Output
				tot.CacheRead += s.CacheRead
				tot.CacheWrite += s.CacheWrite
				tot.Total += s.Total
			}
			t.add(sBold.Render("Total"), format.Money(tot.Input), format.Money(tot.Output), format.Money(tot.CacheRead), format.Money(tot.CacheWrite), sBold.Render(format.Money(tot.Total)))
			if fl.csv {
				return t.csv(e.out)
			}
			heading(e.out, "costs", p.Label())
			t.render(e.out)
			heading(e.out, "by provider", "")
			agentTable(byProvider).render(e.out)
			heading(e.out, "by agent", "")
			agentTable(byAgent).render(e.out)
			now := e.eng.Now()
			month := e.eng.Summary(mustMonth(e))
			fmt.Fprintf(e.out, "\n  Month to date %s · projected %s by %s\n", sBold.Render(format.Money(month.Cost)),
				sBold.Render(format.Money(analytics.ProjectMonth(month.Cost, now))), analytics.PeriodFor("month", now).From.AddDate(0, 1, -1).Format("Jan 2"))
			footnotes(e, byAgent)
			return nil
		}),
	}
}

func mustMonth(e *env) []model.Record {
	recs, _ := e.eng.Records(analytics.PeriodFor("month", e.eng.Now()), e.filter())
	return recs
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
