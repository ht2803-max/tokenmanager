package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/ht2803-max/tokenmanager/internal/analytics"
	"github.com/ht2803-max/tokenmanager/internal/format"
)

// ---------------------------------------------------------------- 0 limits

type limitRow struct {
	family string
	b      analytics.Block
}

// limitRows is the block history of every family, most recent first per family.
func (m *Model) limitRows() []limitRow {
	var out []limitRow
	for _, r := range m.data.limits {
		for _, b := range r.History {
			out = append(out, limitRow{r.Family, b})
		}
	}
	return out
}

// usedLine is a window's progress bar with its source, or the raw count when
// no limit is known.
func usedLine(x *analytics.Window, barW int) string {
	if x.Limit.Source == analytics.SourceNone {
		return format.Tokens(x.Counted) + " tokens " + stDim.Render("· no limit known")
	}
	f := x.Limit.Fraction
	st := levelStyle(f)
	s := st.Render(format.BlockBar(f, max(4, barW))) + " " + st.Render(format.Percent(f)) + " " + format.Tokens(x.Counted)
	if x.Limit.Tokens > 0 {
		s += "/" + format.Tokens(x.Limit.Tokens)
	}
	return s
}

func outlookLine(x *analytics.Window, now time.Time) string {
	switch {
	case x.Limit.Source == analytics.SourceNone:
		return stDim.Render(x.Outlook(now))
	case x.HitsBeforeReset(now):
		return stWarn.Render(x.Outlook(now))
	}
	return stGood.Render(x.Outlook(now))
}

// familyLines is the status of one family's session and weekly windows.
func familyLines(r analytics.PlanReport, w int, now time.Time) []string {
	kv := func(k, v string) string { return stDim.Render(fmt.Sprintf("%-10s", k)) + v }
	barW := min(30, w-30)
	var lines []string
	if s := r.Session; s != nil {
		src := "estimated"
		if s.Reported {
			src = "reported"
		}
		lines = append(lines,
			kv("Block", fmt.Sprintf("%s→%s · %s left ", s.Start.Local().Format("15:04"), s.End.Local().Format("15:04"),
				stBold.Render(format.Duration(s.Remaining(now))))+stDim.Render("· "+src)),
			kv("Used", usedLine(s, barW)),
			kv("", stDim.Render(s.Limit.Label())),
			kv("Burn", fmt.Sprintf("%s tok/min · %s/h ", format.Tokens(int64(s.Burn.TokensPerMin)), format.Money(s.Burn.CostPerHour))+stDim.Render(s.Burn.Basis)),
			kv("Projected", fmt.Sprintf("%s by %s", format.Tokens(s.Projected), s.End.Local().Format("15:04"))),
			kv("Outlook", outlookLine(s, now)))
	} else {
		lines = append(lines, kv("Block", stDim.Render("no active block")))
	}
	wk := &r.Weekly
	when := "rolling 7 days"
	if !wk.Rolling {
		when = "resets " + wk.End.Local().Format("Mon 15:04") + " · in " + format.Duration(wk.Remaining(now))
	}
	lines = append(lines, "",
		kv("Weekly", when),
		kv("Used", usedLine(wk, barW)),
		kv("", stDim.Render(wk.Limit.Label())),
		kv("Outlook", outlookLine(wk, now)))
	if r.Snapshot != nil {
		lines = append(lines, stDim.Render(fmt.Sprintf("%s rate_limits as of %s", r.Snapshot.Agent, format.Ago(r.Snapshot.Observed, now))))
	}
	return lines
}

func (m *Model) limitsView(h int) string {
	w := m.w
	reps := m.data.limits
	now := m.eng.Now()
	if len(reps) == 0 {
		body := "No Claude Code or Codex usage in the last 30 days.\n\n" +
			stDim.Render("Subscription plans are limited by 5 hour blocks and a weekly window.\nSet limits under \"plans:\" in config.yaml; Codex reports its own.")
		return panel("Limits", body, w, h, true)
	}
	title := func(r analytics.PlanReport) string {
		t := "Current block · " + r.Family
		if r.Plan != "" {
			t += " (" + r.Plan + ")"
		}
		return t
	}
	// Status panels side by side when wide, stacked otherwise.
	var top string
	if w >= 100 {
		ph := min(14, max(6, h/2))
		cw := w / len(reps)
		var ps []string
		for i, r := range reps {
			pw := cw
			if i == len(reps)-1 {
				pw = w - cw*(len(reps)-1)
			}
			ps = append(ps, panel(title(r), strings.Join(familyLines(r, pw-4, now), "\n"), pw, ph, false))
		}
		top = lipgloss.JoinHorizontal(lipgloss.Top, ps...)
	} else {
		ph := max(4, min(14, (h-5)/len(reps)))
		var ps []string
		for _, r := range reps {
			ps = append(ps, panel(title(r), strings.Join(familyLines(r, w-4, now), "\n"), w, ph, false))
		}
		top = lipgloss.JoinVertical(lipgloss.Left, ps...)
	}
	rest := h - lipgloss.Height(top)
	if rest < 5 {
		return top
	}
	cols := []col{{"Family", 9, false}, {"Start", 15, false}, {"Window", 11, false}, {"Req", 5, true}, {"Counted", 8, true}, {"Total", 7, true}, {"Cost", 9, true}, {"Top model", 0, false}}
	var rows [][]string
	for _, lr := range m.limitRows() {
		b := lr.b
		start := "  " + b.Start.Local().Format("Jan 2 15:04")
		if b.Active {
			start = stGood.Render("● ") + b.Start.Local().Format("Jan 2 15:04")
		}
		rows = append(rows, []string{lr.family, start, b.Start.Local().Format("15:04") + "–" + b.End.Local().Format("15:04"),
			format.Thousands(b.Requests), format.Tokens(b.Counted), format.Tokens(b.Tokens()), costStr(b.Cost, b.Unpriced), b.TopModel()})
	}
	hist := table(cols, rows, m.cursor[vLimits], w-4, rest-3)
	note := stDim.Render(fmt.Sprintf("Counted: %s · monitoring only · costs are API-equivalent", strings.Join(m.cfg.Plans.CountFields(), "+")))
	return lipgloss.JoinVertical(lipgloss.Left, top, panel("Recent blocks", hist+"\n"+note, w, rest, true))
}

// blockSummary is the compact Overview line for one family.
func blockSummary(r analytics.PlanReport, w int, now time.Time) string {
	name := fit(r.Family, 10)
	s := r.Session
	if s == nil {
		wk := &r.Weekly
		return name + stDim.Render("no active block · weekly ") + usedLine(wk, 10) + " " + stDim.Render(wk.Limit.Label())
	}
	return name + usedLine(s, min(20, max(6, w-70))) + stDim.Render(" · ") +
		format.Tokens(int64(s.Burn.TokensPerMin)) + stDim.Render("/min · ") + outlookLine(s, now) +
		stDim.Render(" · resets in "+format.Duration(s.Remaining(now))+" · "+s.Limit.Label())
}

// blockPanel is the Overview's "Current block" panel (one line per family).
func (m *Model) blockPanel(w int) string {
	now := m.eng.Now()
	var lines []string
	for _, r := range m.data.limits {
		lines = append(lines, blockSummary(r, w-4, now))
	}
	return panel("Current block · 0 for details", strings.Join(lines, "\n"), w, len(lines)+3, false)
}
