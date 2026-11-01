package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ht2803-max/tokenmanager/internal/analytics"
	"github.com/ht2803-max/tokenmanager/internal/format"
)

type limitJSON struct {
	Tokens       int64      `json:"tokens"`
	Source       string     `json:"source"`
	Estimated    bool       `json:"estimated"`
	Implied      bool       `json:"implied_from_percent,omitempty"`
	UsedFraction float64    `json:"used_fraction"`
	ObservedAt   *time.Time `json:"observed_at,omitempty"`
}

type blockJSON struct {
	Start         time.Time `json:"start"`
	End           time.Time `json:"end"`
	Active        bool      `json:"active"`
	Reported      bool      `json:"boundaries_reported"`
	CountedTokens int64     `json:"counted_tokens"`
	aggJSON
}

type burnJSON struct {
	TokensPerMin float64 `json:"tokens_per_min"`
	CostPerHour  float64 `json:"cost_per_hour"`
	Basis        string  `json:"basis"`
}

type windowJSON struct {
	Kind string `json:"kind"`
	blockJSON
	Rolling         bool      `json:"rolling,omitempty"`
	ElapsedSec      float64   `json:"elapsed_seconds"`
	RemainingSec    float64   `json:"remaining_seconds"`
	Burn            burnJSON  `json:"burn"`
	ProjectedTokens int64     `json:"projected_tokens"`
	Limit           limitJSON `json:"limit"`
	ETASec          *float64  `json:"eta_seconds"` // null when unknown
	LimitReached    bool      `json:"limit_reached"`
	HitsBeforeReset bool      `json:"hits_before_reset"`
}

type planJSON struct {
	Family  string      `json:"family"`
	Plan    string      `json:"plan,omitempty"`
	Agents  []string    `json:"agents"`
	Count   []string    `json:"counted_fields"`
	Session *windowJSON `json:"session"`
	Weekly  windowJSON  `json:"weekly"`
	History []blockJSON `json:"history"`
}

func blockToJSON(b analytics.Block) blockJSON {
	return blockJSON{b.Start, b.End, b.Active, b.Reported, b.Counted, toJSON(&b.Agg)}
}

func windowToJSON(w *analytics.Window, now time.Time) windowJSON {
	j := windowJSON{
		Kind: w.Kind, blockJSON: blockToJSON(w.Block), Rolling: w.Rolling,
		ElapsedSec: now.Sub(w.Start).Seconds(), RemainingSec: w.Remaining(now).Seconds(),
		Burn:            burnJSON{round2(w.Burn.TokensPerMin), round2(w.Burn.CostPerHour), w.Burn.Basis},
		ProjectedTokens: w.Projected,
		Limit: limitJSON{Tokens: w.Limit.Tokens, Source: string(w.Limit.Source), Estimated: w.Limit.Source.Estimated(),
			Implied: w.Limit.Implied, UsedFraction: float64(int64(w.Limit.Fraction*10000+0.5)) / 10000},
		LimitReached: w.Reached, HitsBeforeReset: w.HitsBeforeReset(now),
	}
	if !w.Limit.Observed.IsZero() {
		o := w.Limit.Observed
		j.Limit.ObservedAt = &o
	}
	if w.ETAKnown {
		s := w.ETA.Seconds()
		j.ETASec = &s
	}
	return j
}

// ---------------------------------------------------------------- limits

func limitsCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use: "limits", Aliases: []string{"blocks"}, Args: cobra.NoArgs,
		Short: "Show subscription usage windows: the current 5h block, the weekly window and recent blocks",
		Long: `Claude Pro/Max and ChatGPT Plus/Pro are limited by usage windows: a 5 hour
session block that starts with the first request after the previous block
ended, and a weekly cap. This command shows where you stand in each.

Every limit names its source: "reported" (Codex rate_limits), "configured"
(plans: in config.yaml), "personal max" (your largest completed block in the
last 30 days, an estimate) or "no limit known". Limits are monitoring only.`,
		RunE: run(func(e *env, _ []string) error {
			reps, err := e.eng.Limits(e.filter())
			if err != nil {
				return err
			}
			now := e.eng.Now()
			for i := range reps {
				if limit > 0 && len(reps[i].History) > limit {
					reps[i].History = reps[i].History[:limit]
				}
			}
			if fl.json {
				out := make([]planJSON, 0, len(reps))
				for _, r := range reps {
					p := planJSON{Family: r.Family, Plan: r.Plan, Agents: r.Agents, Count: e.cfg.Plans.CountFields(),
						Weekly: windowToJSON(&r.Weekly, now), History: []blockJSON{}}
					if r.Session != nil {
						s := windowToJSON(r.Session, now)
						p.Session = &s
					}
					for _, b := range r.History {
						p.History = append(p.History, blockToJSON(b))
					}
					out = append(out, p)
				}
				return writeJSON(e.out, map[string]any{"generated_at": now, "currency": format.Currency, "plans": out})
			}
			t := newTable("Family", "Start", "End", "Active", "Requests", "Counted", "Total", "Cost", "Top model")
			t.alignRight(4, 5, 6, 7)
			for _, r := range reps {
				for _, b := range r.History {
					t.add(r.Family, b.Start.Local().Format("2006-01-02 15:04"), b.End.Local().Format("2006-01-02 15:04"), fmt.Sprint(b.Active),
						fmt.Sprint(b.Requests), fmt.Sprint(b.Counted), fmt.Sprint(b.Tokens()), fmt.Sprintf("%.2f", b.Cost), b.TopModel())
				}
			}
			if fl.csv {
				return t.csv(e.out)
			}
			if len(reps) == 0 {
				heading(e.out, "limits", "")
				fmt.Fprintln(e.out, sDim.Render("  No Claude Code or Codex usage in the last 30 days."))
				return nil
			}
			for _, r := range reps {
				limitsText(e, r, limit, now)
			}
			fmt.Fprintln(e.out)
			fmt.Fprintln(e.out, sDim.Render(fmt.Sprintf("  Counted tokens: %s. Costs are API-equivalent value, not what a subscription bills.",
				strings.Join(e.cfg.Plans.CountFields(), "+"))))
			fmt.Fprintln(e.out, sDim.Render("  Monitoring only: TokenManager never changes provider-side limits."))
			return nil
		}),
	}
	cmd.Flags().IntVarP(&limit, "limit", "n", 10, "recent blocks to show (0 = all in the last 30 days)")
	return cmd
}

func limitsText(e *env, r analytics.PlanReport, n int, now time.Time) {
	w := e.out
	sub := strings.Join(r.Agents, ", ")
	if r.Plan != "" {
		sub = r.Plan + " · " + sub
	}
	heading(w, "limits · "+r.Family, sub)
	row := func(k, v string) { fmt.Fprintf(w, "  %-11s %s\n", k, v) }
	tm := func(t time.Time) string { return t.Local().Format("Mon Jan 2 15:04") }
	window := func(x *analytics.Window) {
		f := x.Limit.Fraction
		used := sBold.Render(format.Tokens(x.Counted)) + " tokens"
		if x.Limit.Tokens > 0 {
			used += " of " + format.Tokens(x.Limit.Tokens)
		}
		if x.Limit.Source != analytics.SourceNone {
			used = usageStyle(f).Render(format.BlockBar(f, 20)) + " " + usageStyle(f).Render(format.Percent(f)) + "  " + used
		}
		row("Used", used+"  "+sDim.Render("("+x.Limit.Label()+")"))
		row("Burn rate", fmt.Sprintf("%s tok/min · %s/h  %s", format.Tokens(int64(x.Burn.TokensPerMin)),
			format.Money(x.Burn.CostPerHour), sDim.Render("("+x.Burn.Basis+")")))
		if !x.Rolling {
			row("Projected", fmt.Sprintf("%s tokens by %s", format.Tokens(x.Projected), x.End.Local().Format("Mon 15:04")))
		}
		style := sGood
		if x.HitsBeforeReset(now) {
			style = sWarn
		}
		if x.Limit.Source == analytics.SourceNone {
			style = sDim
		}
		row("Outlook", style.Render(x.Outlook(now)))
	}

	if s := r.Session; s != nil {
		src := "estimated from request times"
		if s.Reported {
			src = "reported by provider"
		}
		row("Block", fmt.Sprintf("%s → %s  %s elapsed · %s left  %s", s.Start.Local().Format("15:04"), s.End.Local().Format("15:04"),
			format.Duration(now.Sub(s.Start)), sBold.Render(format.Duration(s.Remaining(now))), sDim.Render("("+src+")")))
		window(s)
	} else {
		row("Block", sDim.Render("no active block: the next request starts a new one"))
	}
	fmt.Fprintln(w)
	wk := &r.Weekly
	switch {
	case wk.Rolling:
		row("Weekly", "rolling 7 days since "+tm(wk.Start)+sDim.Render("  (set plans."+r.Family+".weekly_reset for a fixed reset)"))
	default:
		src := "configured reset"
		if wk.Reported {
			src = "reported by provider"
		}
		row("Weekly", fmt.Sprintf("since %s · resets %s (in %s)  %s", tm(wk.Start), tm(wk.End), format.Duration(wk.Remaining(now)), sDim.Render("("+src+")")))
	}
	window(wk)
	if snap := r.Snapshot; snap != nil {
		row("Report", sDim.Render(fmt.Sprintf("%s rate_limits as of %s", snap.Agent, format.Ago(snap.Observed, now))))
	}

	if len(r.History) == 0 {
		return
	}
	heading(w, "recent blocks · "+r.Family, "")
	t := newTable("Start", "Window", "Requests", "Counted", "Total", "Cost", "Top model")
	t.alignRight(2, 3, 4, 5)
	for _, b := range r.History {
		mark := ""
		if b.Active {
			mark = sGood.Render(" ●")
		}
		t.add(b.Start.Local().Format("Mon Jan 2 15:04")+mark, b.Start.Local().Format("15:04")+"–"+b.End.Local().Format("15:04"),
			format.Thousands(b.Requests), format.Tokens(b.Counted), format.Tokens(b.Tokens()), costCell(b.Cost, b.Unpriced), b.TopModel())
	}
	t.render(w)
}
