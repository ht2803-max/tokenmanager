package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/ht2803-max/tokenmanager/internal/analytics"
	"github.com/ht2803-max/tokenmanager/internal/config"
	"github.com/ht2803-max/tokenmanager/internal/format"
	"github.com/ht2803-max/tokenmanager/internal/model"
	"github.com/ht2803-max/tokenmanager/internal/optimizer"
)

func (m *Model) View() string {
	if m.w == 0 {
		return ""
	}
	if m.w < 60 || m.h < 18 {
		return lipgloss.Place(m.w, m.h, lipgloss.Center, lipgloss.Center,
			stDim.Render(fmt.Sprintf("Terminal too small (%dx%d).\nTokenManager needs at least 60x18.", m.w, m.h)))
	}
	header := m.header()
	footer := m.footer()
	bodyH := m.h - lipgloss.Height(header) - lipgloss.Height(footer)
	var body string
	switch {
	case m.data == nil:
		body = lipgloss.Place(m.w, bodyH, lipgloss.Center, lipgloss.Center, stDim.Render("Loading usage…"))
	case m.data.err != nil:
		body = lipgloss.Place(m.w, bodyH, lipgloss.Center, lipgloss.Center, stBad.Render("Error: "+m.data.err.Error()))
	case m.help:
		body = m.helpView(bodyH)
	default:
		switch m.view {
		case vOverview:
			body = m.overview(bodyH)
		case vUsage:
			body = m.usage(bodyH)
		case vAgents:
			body = m.agentsView(bodyH)
		case vSessions:
			body = m.sessionsView(bodyH)
		case vProjects:
			body = m.projectsView(bodyH)
		case vModels:
			body = m.modelsView(bodyH)
		case vOptimize:
			body = m.optimizeView(bodyH)
		case vBudgets:
			body = m.budgetsView(bodyH)
		case vSettings:
			body = m.settingsView(bodyH)
		}
	}
	body = lipgloss.NewStyle().Height(bodyH).MaxHeight(bodyH).Render(body)
	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
}

// ---------------------------------------------------------------- chrome

func (m *Model) header() string {
	logo := stLogo.Render("◆ TokenManager")
	var tabs []string
	for i, n := range viewNames {
		label := fmt.Sprintf("%d %s", i+1, n)
		if m.w < 120 {
			label = fmt.Sprintf("%d %s", i+1, n[:min(len(n), 4)])
		}
		if i == m.view {
			tabs = append(tabs, stTabOn.Render(label))
		} else {
			tabs = append(tabs, stTabOff.Render(label))
		}
	}
	left := logo + " " + strings.Join(tabs, "")
	right := ""
	if m.data != nil {
		right = stAccent.Render(m.data.period.Label())
	}
	live := stDim.Render(" ○ idle")
	if time.Since(m.flash) < 3*time.Second {
		live = stGood.Render(" ● live")
	} else if m.loading {
		live = stWarn.Render(" ◌ sync")
	}
	right += live + " "
	gap := m.w - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return fit(left, m.w)
	}
	return left + strings.Repeat(" ", gap) + right
}

func (m *Model) footer() string {
	if m.searching {
		return fit(" "+m.search.View(), m.w)
	}
	keys := []string{"1-9 views", "j/k move", "tab panel", "enter details", "f period", "/ search", "r refresh", "? help", "q quit"}
	var parts []string
	for _, k := range keys {
		sp := strings.SplitN(k, " ", 2)
		parts = append(parts, stKey.Render(sp[0])+" "+stDim.Render(sp[1]))
	}
	left := " " + strings.Join(parts, stDim.Render(" · "))
	if q := m.search.Value(); q != "" {
		left = " " + stWarn.Render("filter: "+q) + stDim.Render(" (esc clears) · ") + strings.Join(parts[:3], stDim.Render(" · "))
	}
	right := ""
	if m.data != nil {
		right = stDim.Render(fmt.Sprintf("updated %s ", m.data.loadedAt.Format("15:04:05")))
	}
	gap := m.w - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return fit(left, m.w)
	}
	return left + strings.Repeat(" ", gap) + right
}

func (m *Model) helpView(h int) string {
	rows := [][2]string{
		{"1-9", "jump to view (Overview, Usage, Agents, Sessions, Projects, Models, Optimize, Budgets, Settings)"},
		{"←/→  h/l", "previous / next view"},
		{"j/k  ↑/↓", "move in lists · pgup/pgdn page · g/G top/bottom"},
		{"tab", "switch panel (Overview) · toggle detail pane"},
		{"enter", "open details"},
		{"esc", "close details / clear filter"},
		{"f / F", "cycle date filter: today → week → month → 30 days → all"},
		{"/", "search the current list"},
		{"r", "rescan agent files and refresh"},
		{"?", "toggle this help"},
		{"q", "quit"},
	}
	var b strings.Builder
	for _, r := range rows {
		b.WriteString(stKey.Render(fmt.Sprintf("%-10s", r[0])) + "  " + stText.Render(r[1]) + "\n")
	}
	b.WriteString("\n" + stDim.Render("Data is read locally from agent session files; nothing is uploaded.\nCosts are estimates at API list prices. Token counts marked ~ are estimated."))
	w := min(m.w-4, 100)
	return lipgloss.Place(m.w, h, lipgloss.Center, lipgloss.Center, panel("Keyboard shortcuts", b.String(), w, min(h, len(rows)+6), true))
}

// ---------------------------------------------------------------- shared bits

func costStr(cost float64, unpriced int64) string {
	if unpriced > 0 && cost == 0 {
		return stDim.Render("n/a")
	}
	s := format.Money(cost)
	if unpriced > 0 {
		s += stWarn.Render("*")
	}
	return s
}

func statCard(title, big, sub, extra string, w int) string {
	body := stBig.Render(big) + "\n" + stDim.Render(sub)
	if extra != "" {
		body += "\n" + extra
	}
	return panel(title, body, w, 6, false)
}

// barList renders labelled horizontal bars for aggregates (share of cost,
// falling back to tokens when nothing is priced).
func barList(gs []*analytics.Agg, w, rows int, label func(*analytics.Agg) string, color func(int, *analytics.Agg) lipgloss.Style) string {
	var total float64
	useCost := false
	for _, g := range gs {
		if g.Cost > 0 {
			useCost = true
		}
	}
	val := func(g *analytics.Agg) float64 {
		if useCost {
			return g.Cost
		}
		return float64(g.Tokens())
	}
	for _, g := range gs {
		total += val(g)
	}
	labelW := min(22, max(10, w/3))
	barW := max(4, w-labelW-22)
	var lines []string
	for i, g := range gs {
		if i >= rows {
			break
		}
		f := 0.0
		if total > 0 {
			f = val(g) / total
		}
		lines = append(lines, fit(label(g), labelW)+" "+color(i, g).Render(format.Bar(f, barW))+" "+
			padLeft(format.Tokens(g.Tokens()), 7)+" "+padLeft(costStr(g.Cost, g.Unpriced), 9))
	}
	if len(lines) == 0 {
		return stDim.Render("No usage in this period.")
	}
	return strings.Join(lines, "\n")
}

func paletteStyle(i int, _ *analytics.Agg) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(palette[i%len(palette)])
}

func agentColor(_ int, g *analytics.Agg) lipgloss.Style { return agentStyle(g.Key) }

// columnChart draws token columns for a series with a value axis and labels.
func columnChart(pts []analytics.Point, w, h int, hourly bool) string {
	if len(pts) == 0 || h < 3 {
		return stDim.Render("No data.")
	}
	axisW := 7
	avail := w - axisW - 1
	// Each point gets an equal slot; slots are 1-3 cells wide.
	slot := max(1, min(8, avail/len(pts)))
	if len(pts) > avail {
		pts = pts[len(pts)-avail:]
		slot = 1
	}
	vals := make([]float64, len(pts))
	hi := 0.0
	for i, p := range pts {
		vals[i] = float64(p.Tokens)
		hi = max(hi, vals[i])
	}
	chartH := h - 1
	rows := format.Columns(vals, chartH)
	var out []string
	for r, row := range rows {
		axis := ""
		switch r {
		case 0:
			axis = format.Tokens(int64(hi))
		case chartH - 1:
			axis = "0"
		}
		var b strings.Builder
		for _, ch := range row {
			cell := string(ch)
			if slot > 1 {
				cell = strings.Repeat(cell, slot-1) + " "
			}
			b.WriteString(cell)
		}
		out = append(out, stDim.Render(padLeft(axis, axisW))+" "+stAccent.Render(b.String()))
	}
	// x labels: first, middle, last
	lab := make([]rune, len(pts)*slot)
	for i := range lab {
		lab[i] = ' '
	}
	put := func(i int, s string) {
		pos := i * slot
		for j, r := range s {
			if pos+j < len(lab) {
				lab[pos+j] = r
			}
		}
	}
	label := func(p analytics.Point) string {
		if hourly {
			return p.Start.Format("15h")
		}
		return p.Start.Format("Jan 2")
	}
	put(0, label(pts[0]))
	if len(pts) > 12 {
		put(len(pts)/2-2, label(pts[len(pts)/2]))
	}
	last := label(pts[len(pts)-1])
	put(max(0, len(pts)-len(last)/slot-1), last)
	out = append(out, strings.Repeat(" ", axisW+1)+stDim.Render(string(lab)))
	return strings.Join(out, "\n")
}

// distribution renders the token-category split for an aggregate.
func distribution(a analytics.Agg, w int) string {
	tot := float64(a.Tokens())
	if tot == 0 {
		return stDim.Render("No usage in this period.")
	}
	type part struct {
		name string
		v    int64
		c    lipgloss.Color
	}
	parts := []part{{"Input", a.Input, "#60A5FA"}, {"Cache read", a.CacheRead, "#4ADE80"}, {"Cache write", a.CacheWrite, "#FBBF24"}, {"Output", a.Output, "#F472B6"}}
	barW := max(10, w)
	var bar strings.Builder
	used := 0
	for i, p := range parts {
		n := int(float64(p.v) / tot * float64(barW))
		if i == len(parts)-1 {
			n = barW - used
		}
		if p.v > 0 && n == 0 && used < barW {
			n = 1
		}
		n = max(0, min(n, barW-used))
		used += n
		bar.WriteString(lipgloss.NewStyle().Foreground(p.c).Render(strings.Repeat("█", n)))
	}
	lines := []string{bar.String()}
	for _, p := range parts {
		lines = append(lines, lipgloss.NewStyle().Foreground(p.c).Render("■ ")+fit(p.name, 12)+padLeft(format.Tokens(p.v), 8)+"  "+stDim.Render(padLeft(format.Percent(float64(p.v)/tot), 6)))
	}
	io := float64(a.Output) / float64(max(a.Prompt(), 1))
	lines = append(lines, stDim.Render(fmt.Sprintf("Output/input ratio %.3f · cache hit %s", io, format.Percent(a.CacheHitRate()))))
	return strings.Join(lines, "\n")
}

func ctxBar(used, window int64, w int) string {
	if window <= 0 {
		return stDim.Render(format.Tokens(used) + " (window unknown)")
	}
	f := float64(used) / float64(window)
	return levelStyle(f/0.8*0.8).Render(format.BlockBar(f, w)) + " " + format.Tokens(used) + "/" + format.Tokens(window)
}

// ---------------------------------------------------------------- 1 overview

func (m *Model) overview(h int) string {
	d := m.data
	o := d.ov
	w := m.w
	cards := 4
	if w < 100 {
		cards = 2
	}
	cw := w / cards
	month := ""
	if o.MonthBudget > 0 {
		f := o.Month.Cost / o.MonthBudget
		month = levelStyle(f).Render(format.BlockBar(f, max(6, cw-14))) + " " + format.Percent(f)
	}
	proj := "at current pace"
	if o.MonthBudget > 0 {
		proj = "budget " + format.Money(o.MonthBudget)
	}
	projBar := ""
	if o.MonthBudget > 0 {
		pf := o.ProjectedMonth / o.MonthBudget
		projBar = levelStyle(pf).Render(format.BlockBar(pf, max(6, cw-14))) + " " + format.Percent(pf)
	}
	cs := []string{
		statCard("Today", costStr(o.Today.Cost, o.Today.Unpriced), fmt.Sprintf("%s tokens · %d req", format.Tokens(o.Today.Tokens()), o.Today.Requests), sparkToday(d, cw-4), cw),
		statCard("This week", costStr(o.Week.Cost, o.Week.Unpriced), fmt.Sprintf("%s tokens · %d sessions", format.Tokens(o.Week.Tokens()), o.Week.Sessions()), stDim.Render("in "+format.Tokens(o.Week.Input)+" · out "+format.Tokens(o.Week.Output)), cw),
		statCard("This month", costStr(o.Month.Cost, o.Month.Unpriced), fmt.Sprintf("%s tokens · cache %s", format.Tokens(o.Month.Tokens()), format.Tokens(o.Month.CacheRead)), month, cw),
		statCard("Projected month", format.Money(o.ProjectedMonth), proj, projBar, w-cw*(cards-1)),
	}
	var top string
	if cards == 4 {
		top = lipgloss.JoinHorizontal(lipgloss.Top, cs...)
	} else {
		top = lipgloss.JoinVertical(lipgloss.Left,
			lipgloss.JoinHorizontal(lipgloss.Top, cs[0], cs[1]),
			lipgloss.JoinHorizontal(lipgloss.Top, cs[2], statCard("Projected month", format.Money(o.ProjectedMonth), proj, projBar, w-cw)))
	}
	rest := h - lipgloss.Height(top)
	if rest < 8 {
		return top
	}
	midH := rest / 2
	botH := rest - midH
	if w >= 100 {
		lw := w * 3 / 5
		rw := w - lw
		chart := panel("Token consumption · last 30 days", columnChart(d.daily, lw-4, midH-3, false), lw, midH, false)
		agentsP := panel("Agents · "+d.period.Name, m.agentLines(rw-4, midH-3), rw, midH, m.focus == 1)
		sess := panel("Recent sessions · "+d.period.Name, m.recentSessions(lw-4, botH-3), lw, botH, m.focus == 0)
		br := panel("Breakdown · "+d.period.Name, m.breakdown(rw-4, botH-3), rw, botH, false)
		return lipgloss.JoinVertical(lipgloss.Left, top,
			lipgloss.JoinHorizontal(lipgloss.Top, chart, agentsP),
			lipgloss.JoinHorizontal(lipgloss.Top, sess, br))
	}
	chart := panel("Token consumption · last 30 days", columnChart(d.daily, w-4, midH-3, false), w, midH, false)
	sess := panel("Recent sessions", m.recentSessions(w-4, botH-3), w, botH, m.focus == 0)
	return lipgloss.JoinVertical(lipgloss.Left, top, chart, sess)
}

func sparkToday(d *snapshot, w int) string {
	pts := make([]float64, len(d.hourly))
	for i, p := range d.hourly {
		pts[i] = float64(p.Tokens)
	}
	return stAccent.Render(fit(format.Sparkline(pts), w))
}

func (m *Model) agentLines(w, rows int) string {
	d := m.data
	active := activeByAgent(d.sessions)
	var lines []string
	var total float64
	for _, g := range d.byAgent {
		total += g.Cost
	}
	cursor := -1
	if m.focus == 1 {
		cursor = m.cursor[vOverview]
	}
	for i, g := range d.byAgent {
		if len(lines) >= rows-1 {
			break
		}
		f := 0.0
		if total > 0 {
			f = g.Cost / total
		}
		dot := stDim.Render("○")
		if active[g.Key] > 0 {
			dot = stGood.Render("●")
		}
		name := dot + " " + agentStyle(g.Key).Render(fit(g.Key, 12))
		line := name + " " + agentStyle(g.Key).Render(format.Bar(f, max(4, w-46))) + " " + padLeft(format.Tokens(g.Tokens()), 7) + " " + padLeft(costStr(g.Cost, g.Unpriced), 9) + " " + stDim.Render(fit(g.TopModel(), 16))
		if i == cursor {
			line = stSelected.Render(stripANSI(fit(line, w)))
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		lines = append(lines, stDim.Render("No agent usage in this period."))
	}
	n := 0
	for _, v := range active {
		n += v
	}
	lines = append(lines, stDim.Render(fmt.Sprintf("%d active session(s) in the last 30 min", n)))
	return strings.Join(lines, "\n")
}

func activeByAgent(ss []*analytics.Session) map[string]int {
	out := map[string]int{}
	cut := time.Now().Add(-30 * time.Minute)
	for _, s := range ss {
		if s.Last.After(cut) {
			out[s.Agent]++
		}
	}
	return out
}

func (m *Model) recentSessions(w, h int) string {
	cols := []col{{"When", 11, false}, {"Agent", 11, false}, {"Project", 0, false}, {"Dur", 6, true}, {"Tokens", 7, true}, {"Cost", 9, true}}
	var rows [][]string
	for i, s := range m.data.sessions {
		if i >= 50 {
			break
		}
		rows = append(rows, []string{format.Ago(s.Last, time.Now()), agentStyle(s.Agent).Render(s.Agent), format.TruncateLeft(analytics.ShortProject(s.Project), 40),
			format.Duration(s.Duration()), format.Tokens(s.Tokens()), costStr(s.Cost, s.Unpriced)})
	}
	if len(rows) == 0 {
		return stDim.Render("No sessions in this period.")
	}
	cursor := -1
	if m.focus == 0 {
		cursor = m.cursor[vOverview]
	}
	return table(cols, rows, cursor, w, h)
}

func (m *Model) breakdown(w, h int) string {
	d := m.data
	sum := m.eng.Summary(d.recs)
	out := distribution(sum, w)
	used := lipgloss.Height(out)
	if h-used > 3 {
		out += "\n\n" + stBold.Render("By provider") + "\n" + barList(d.byProvider, w, min(3, h-used-3), func(g *analytics.Agg) string { return g.Key }, paletteStyle)
	}
	used = lipgloss.Height(out)
	if h-used > 3 {
		out += "\n\n" + stBold.Render("By model") + "\n" + barList(d.byModel, w, h-used-2, func(g *analytics.Agg) string { return g.Key }, paletteStyle)
	}
	return out
}

// ---------------------------------------------------------------- 2 usage

func (m *Model) usage(h int) string {
	d := m.data
	w := m.w
	sum := m.eng.Summary(d.recs)
	avg := 0.0
	if sum.Requests > 0 {
		avg = sum.Cost / float64(sum.Requests)
	}
	stats := []string{
		"Tokens " + stBold.Render(format.Tokens(sum.Tokens())),
		"Cost " + stBold.Render(costStr(sum.Cost, sum.Unpriced)),
		"Requests " + stBold.Render(format.Thousands(sum.Requests)),
		"Sessions " + stBold.Render(fmt.Sprint(sum.Sessions())),
		"Cache hit " + stBold.Render(format.Percent(sum.CacheHitRate())),
		"Avg/request " + stBold.Render(format.Money(avg)),
		"Peak prompt " + stBold.Render(format.Tokens(sum.MaxPrompt)),
	}
	statLine := " " + strings.Join(stats, stDim.Render("  │  "))
	chartH := max(8, (h-1)/2)
	hourly := d.period.Name == "today"
	title := "Tokens per day · " + d.period.Label()
	if hourly {
		title = "Tokens per hour · today"
	}
	costs := make([]float64, len(d.series))
	for i, p := range d.series {
		costs[i] = p.Cost
	}
	chartBody := columnChart(d.series, w-4, chartH-4, hourly) + "\n" + stDim.Render("cost    ") + stCyan.Render(fit(format.Sparkline(costs), w-12))
	chart := panel(title, chartBody, w, chartH, false)
	bh := h - 1 - chartH
	if bh < 6 {
		return lipgloss.JoinVertical(lipgloss.Left, fit(statLine, w), chart)
	}
	third := w / 3
	dist := panel("Input vs output", distribution(sum, third-4), third, bh, false)
	prov := panel("By provider", barList(d.byProvider, third-4, bh-3, func(g *analytics.Agg) string { return g.Key }, paletteStyle), third, bh, false)
	mod := panel("By model", barList(d.byModel, w-2*third-4, bh-3, func(g *analytics.Agg) string { return g.Key }, paletteStyle), w-2*third, bh, false)
	return lipgloss.JoinVertical(lipgloss.Left, fit(statLine, w), chart, lipgloss.JoinHorizontal(lipgloss.Top, dist, prov, mod))
}

// ---------------------------------------------------------------- 3 agents

func (m *Model) agentsView(h int) string {
	d := m.data
	w := m.w
	groups := map[string]*analytics.Agg{}
	for _, g := range d.byAgent {
		groups[g.Key] = g
	}
	active := activeByAgent(d.sessions)
	latestSession := map[string]*analytics.Session{}
	for _, s := range d.sessions {
		if latestSession[s.Agent] == nil {
			latestSession[s.Agent] = s
		}
	}
	cols := []col{{"Agent", 14, false}, {"Status", 10, false}, {"Data", 11, false}, {"Active", 6, true}, {"Sessions", 8, true}, {"Tokens", 8, true}, {"Cost", 10, true}, {"Model", 0, false}, {"Context (latest)", 26, false}}
	var rows [][]string
	for _, st := range d.agentSt {
		a := st.Adapter
		status := stDim.Render("not found")
		if !st.Enabled {
			status = stDim.Render("disabled")
		} else if st.Installed {
			status = stGood.Render("detected")
		}
		data := map[model.Accuracy]string{model.Actual: stGood.Render("actual"), model.Estimated: stWarn.Render("~estimated"), model.Unavailable: stDim.Render("unavailable")}[a.Availability]
		row := []string{agentStyle(a.Name).Render(a.Display), status, data, "—", "—", "—", "—", "—", "—"}
		if g := groups[a.Name]; g != nil {
			row[3] = fmt.Sprint(active[a.Name])
			row[4] = fmt.Sprint(g.Sessions())
			row[5] = format.Tokens(g.Tokens())
			row[6] = costStr(g.Cost, g.Unpriced)
			row[7] = g.TopModel()
		}
		if s := latestSession[a.Name]; s != nil {
			row[8] = ctxBar(s.LastPrompt, s.ContextWindow, 8)
		}
		rows = append(rows, row)
	}
	tH := len(rows) + 4
	tbl := panel("Agents · "+d.period.Label(), table(cols, rows, m.cursor[vAgents], w-4, len(rows)+1)+"\n"+m.agentNote(), w, tH+2, true)
	feedH := h - lipgloss.Height(tbl)
	if feedH < 5 {
		return tbl
	}
	return lipgloss.JoinVertical(lipgloss.Left, tbl, panel("Latest requests · live", m.feed(w-4, feedH-3), w, feedH, false))
}

func (m *Model) agentNote() string {
	d := m.data
	i := m.cursor[vAgents]
	if i >= len(d.agentSt) {
		return ""
	}
	st := d.agentSt[i]
	roots := "no data directory found"
	if len(st.Roots) > 0 {
		roots = strings.Join(st.Roots, ", ")
	}
	return stDim.Render(st.Adapter.Note) + "\n" + stDim.Render(fmt.Sprintf("Sources: %s · %d files", roots, len(st.Files)))
}

func (m *Model) feed(w, h int) string {
	recs := m.data.latest
	cols := []col{{"Time", 8, false}, {"Agent", 11, false}, {"Model", 18, false}, {"Input", 7, true}, {"Output", 7, true}, {"Cache R", 7, true}, {"Cache W", 7, true}, {"Cost", 9, true}, {"Session", 0, false}}
	var rows [][]string
	fresh := map[string]bool{}
	for _, r := range m.live {
		fresh[r.Key] = true
	}
	for i := len(recs) - 1; i >= 0 && len(rows) < h; i-- {
		r := recs[i]
		cost := stDim.Render("n/a")
		if c, ok := m.eng.Prices.Cost(r); ok {
			cost = format.Money(c)
		}
		t := r.Timestamp.Local().Format("15:04:05")
		if r.Timestamp.Before(analytics.PeriodFor("today", time.Now()).From) {
			t = r.Timestamp.Local().Format("Jan 02")
		}
		if fresh[r.Key] {
			t = stGood.Render(t)
		}
		in := format.Tokens(r.Input)
		if r.Accuracy == model.Estimated {
			in = "~" + in
		}
		rows = append(rows, []string{t, agentStyle(r.Agent).Render(r.Agent), r.Model, in, format.Tokens(r.Output), format.Tokens(r.CacheRead), format.Tokens(r.CacheWrite()), cost,
			stDim.Render(format.TruncateLeft(analytics.ShortProject(r.Project), 30))})
	}
	if len(rows) == 0 {
		return stDim.Render("No requests in the last 30 days. New requests appear here as agents write them.")
	}
	return table(cols, rows, -1, w, h)
}

// ---------------------------------------------------------------- 4 sessions

func (m *Model) sessionsView(h int) string {
	w := m.w
	ss := m.filteredSessions()
	cols := []col{{"Last active", 12, false}, {"Agent", 11, false}, {"Project", 0, false}, {"Model", 18, false}, {"Dur", 7, true}, {"Req", 5, true}, {"Tokens", 7, true}, {"Context", 7, true}, {"Cost", 9, true}}
	var rows [][]string
	for _, s := range ss {
		ctx := format.Tokens(s.LastPrompt)
		if s.ContextWindow > 0 {
			ctx = format.Percent(float64(s.LastPrompt) / float64(s.ContextWindow))
		}
		rows = append(rows, []string{s.Last.Local().Format("Jan 02 15:04"), agentStyle(s.Agent).Render(s.Agent), format.TruncateLeft(analytics.ShortProject(s.Project), 60),
			s.TopModel(), format.Duration(s.Duration()), fmt.Sprint(s.Requests), format.Tokens(s.Tokens()), ctx, costStr(s.Cost, s.Unpriced)})
	}
	title := fmt.Sprintf("Sessions · %s · %d", m.data.period.Label(), len(ss))
	if len(rows) == 0 {
		return panel(title, stDim.Render("No sessions match."), w, h, true)
	}
	if !m.detail {
		return panel(title, table(cols, rows, m.cursor[vSessions], w-4, h-3), w, h, true)
	}
	listH := max(6, h/2)
	list := panel(title, table(cols, rows, m.cursor[vSessions], w-4, listH-3), w, listH, true)
	return lipgloss.JoinVertical(lipgloss.Left, list, panel("Session detail", m.sessionDetail(ss[m.cursor[vSessions]], w-4, h-listH-3), w, h-listH, false))
}

func (m *Model) sessionDetail(s *analytics.Session, w, h int) string {
	var prompts []float64
	for _, r := range m.data.recs {
		if r.SessionID == s.ID && r.Agent == s.Agent {
			prompts = append(prompts, float64(r.PromptTokens()))
		}
	}
	kv := func(k, v string) string { return stDim.Render(fmt.Sprintf("%-14s", k)) + v }
	left := []string{
		kv("Session", s.ID),
		kv("Agent", agentStyle(s.Agent).Render(s.Agent)),
		kv("Project", analytics.ShortProject(s.Project)),
		kv("Models", strings.Join(s.Models(), ", ")),
		kv("Started", s.First.Local().Format("Mon Jan 2 15:04")),
		kv("Duration", format.Duration(s.Duration())+fmt.Sprintf(" · %d requests", s.Requests)),
		kv("Cost", costStr(s.Cost, s.Unpriced)+stDim.Render("  (estimate)")),
		kv("Context", ctxBar(s.LastPrompt, s.ContextWindow, 16)),
		kv("Peak prompt", format.Tokens(s.MaxPrompt)),
	}
	half := w / 2
	right := distribution(s.Agg, half-2) + "\n\n" + stBold.Render("Prompt size per request") + "\n" + stAccent.Render(fit(format.Sparkline(tail(prompts, half-2)), half-2))
	return lipgloss.JoinHorizontal(lipgloss.Top, lipgloss.NewStyle().Width(half).Render(strings.Join(left, "\n")), right)
}

func tail(v []float64, n int) []float64 {
	if len(v) > n {
		return v[len(v)-n:]
	}
	return v
}

// ---------------------------------------------------------------- 5 projects

func (m *Model) projectsView(h int) string {
	gs := m.filteredAggs(m.data.byProject)
	return m.aggView("Projects", gs, h, func(g *analytics.Agg) string { return format.TruncateLeft(analytics.ShortProject(g.Key), 60) }, func(g *analytics.Agg, w, h int) string {
		var ss []*analytics.Session
		for _, s := range m.data.sessions {
			if (s.Project == g.Key) || (s.Project == "" && g.Key == "(unknown)") {
				ss = append(ss, s)
			}
		}
		agentsUsed := map[string]bool{}
		for _, s := range ss {
			agentsUsed[s.Agent] = true
		}
		var an []string
		for a := range agentsUsed {
			an = append(an, a)
		}
		sort.Strings(an)
		head := stDim.Render("Agents ") + strings.Join(an, ", ") + stDim.Render("   Models ") + strings.Join(g.Models(), ", ")
		cols := []col{{"Last active", 12, false}, {"Agent", 11, false}, {"Model", 0, false}, {"Req", 5, true}, {"Tokens", 7, true}, {"Cost", 9, true}}
		var rows [][]string
		for _, s := range ss {
			rows = append(rows, []string{s.Last.Local().Format("Jan 02 15:04"), s.Agent, s.TopModel(), fmt.Sprint(s.Requests), format.Tokens(s.Tokens()), costStr(s.Cost, s.Unpriced)})
		}
		return head + "\n" + table(cols, rows, -1, w, h-1)
	})
}

// aggView is a list of aggregates with share bars and an optional detail pane.
func (m *Model) aggView(name string, gs []*analytics.Agg, h int, label func(*analytics.Agg) string, detail func(*analytics.Agg, int, int) string) string {
	w := m.w
	var total float64
	for _, g := range gs {
		total += g.Cost
	}
	cols := []col{{name[:len(name)-1], 0, false}, {"Sessions", 8, true}, {"Requests", 8, true}, {"Tokens", 8, true}, {"Cache hit", 9, true}, {"Cost", 10, true}, {"Share", 22, false}, {"Last used", 10, true}}
	var rows [][]string
	for i, g := range gs {
		f := 0.0
		if total > 0 {
			f = g.Cost / total
		}
		rows = append(rows, []string{label(g), fmt.Sprint(g.Sessions()), format.Thousands(g.Requests), format.Tokens(g.Tokens()), format.Percent(g.CacheHitRate()),
			costStr(g.Cost, g.Unpriced), paletteStyle(i, g).Render(format.Bar(f, 14)) + " " + padLeft(format.Percent(f), 6), format.Ago(g.Last, time.Now())})
	}
	title := fmt.Sprintf("%s · %s · %d", name, m.data.period.Label(), len(gs))
	if len(rows) == 0 {
		return panel(title, stDim.Render("Nothing matches."), w, h, true)
	}
	if !m.detail || detail == nil {
		return panel(title, table(cols, rows, m.cursor[m.view], w-4, h-3), w, h, true)
	}
	listH := max(6, h/2)
	g := gs[m.cursor[m.view]]
	return lipgloss.JoinVertical(lipgloss.Left,
		panel(title, table(cols, rows, m.cursor[m.view], w-4, listH-3), w, listH, true),
		panel(label(g), detail(g, w-4, h-listH-3), w, h-listH, false))
}

// ---------------------------------------------------------------- 6 models

func (m *Model) modelsView(h int) string {
	w := m.w
	gs := m.filteredAggs(m.data.byModel)
	var total float64
	for _, g := range gs {
		total += g.Cost
	}
	cols := []col{{"Model", 0, false}, {"Req", 7, true}, {"Tokens", 8, true}, {"Cache hit", 9, true}, {"Out/req", 8, true}, {"Cost/req", 9, true}, {"$/M in·out", 12, true}, {"Cost", 10, true}, {"Share", 22, false}}
	var rows [][]string
	for i, g := range gs {
		pr, ok := m.eng.Prices.Lookup(g.Key)
		price := stDim.Render("unpriced")
		if ok {
			price = fmt.Sprintf("%g·%g", pr.Input, pr.Output)
		}
		f := 0.0
		if total > 0 {
			f = g.Cost / total
		}
		rows = append(rows, []string{paletteStyle(i, g).Render("■ ") + g.Key, format.Thousands(g.Requests), format.Tokens(g.Tokens()), format.Percent(g.CacheHitRate()),
			format.Tokens(g.Output / max(g.Requests, 1)), format.Money(g.Cost / float64(max(g.Requests, 1))), price, costStr(g.Cost, g.Unpriced),
			paletteStyle(i, g).Render(format.Bar(f, 14)) + " " + padLeft(format.Percent(f), 6)})
	}
	title := fmt.Sprintf("Models · %s · %d", m.data.period.Label(), len(gs))
	if len(rows) == 0 {
		return panel(title, stDim.Render("Nothing matches."), w, h, true)
	}
	note := stDim.Render("Unpriced models: add USD-per-million prices under \"pricing:\" in " + config.Path())
	listH := h
	if m.detail {
		listH = max(6, h/2)
	}
	list := panel(title, table(cols, rows, m.cursor[vModels], w-4, listH-4)+"\n"+note, w, listH, true)
	if !m.detail {
		return list
	}
	g := gs[m.cursor[vModels]]
	byAgent := map[string]*analytics.Agg{}
	var agentsList []*analytics.Agg
	for _, r := range m.data.recs {
		if r.Model != g.Key {
			continue
		}
		a := byAgent[r.Agent]
		if a == nil {
			a = &analytics.Agg{Key: r.Agent}
			byAgent[r.Agent] = a
			agentsList = append(agentsList, a)
		}
		m.eng.Add(a, r)
	}
	body := lipgloss.JoinHorizontal(lipgloss.Top,
		lipgloss.NewStyle().Width(w/2).Render(distribution(*g, w/2-4)),
		stBold.Render("By agent")+"\n"+barList(agentsList, w/2-4, h-listH-4, func(a *analytics.Agg) string { return a.Key }, agentColor))
	return lipgloss.JoinVertical(lipgloss.Left, list, panel(g.Key, body, w, h-listH, false))
}

// ---------------------------------------------------------------- 7 optimize

func (m *Model) optimizeView(h int) string {
	w := m.w
	d := m.data
	if !d.optimized {
		return panel("Optimization", stDim.Render("Analyzing the last 30 days…"), w, h, true)
	}
	fs := d.findings
	if len(fs) == 0 {
		return panel("Token optimization report · last 30 days", stGood.Render("No issues found. Usage looks efficient."), w, h, true)
	}
	var total float64
	for _, f := range fs {
		total += f.MonthlySavings
	}
	cols := []col{{"Severity", 9, false}, {"Finding", 32, false}, {"Where", 0, false}, {"Est. monthly", 13, true}}
	var rows [][]string
	for _, f := range fs {
		where := analytics.ShortProject(f.Project)
		switch {
		case f.File != "":
			where = analytics.ShortProject(f.File)
		case f.Project == "":
			where = "all projects"
		}
		sav := stDim.Render("—")
		if f.MonthlySavings >= 0.01 {
			sav = stGood.Render(format.Money(f.MonthlySavings))
		}
		rows = append(rows, []string{sevLabel(f.Severity), f.Title, format.TruncateLeft(where, 70), sav})
	}
	listH := min(len(rows)+4, h/2)
	title := fmt.Sprintf("Token optimization report · last 30 days · est. %s/month", format.Money(total))
	list := panel(title, table(cols, rows, m.cursor[vOptimize], w-4, listH-3), w, listH, true)
	f := fs[m.cursor[vOptimize]]
	kv := func(k, v string) string {
		if v == "" {
			return ""
		}
		return stDim.Render(fmt.Sprintf("%-18s", k)) + v + "\n"
	}
	project := ""
	if f.Project != "" {
		project = analytics.ShortProject(f.Project)
	}
	body := kv("Project", project) + kv("File", f.File) + kv("Finding", wrap(f.Detail, w-24, 18))
	if f.TokensPerSession > 0 {
		body += kv("Potential savings", fmt.Sprintf("~%s tokens per session", format.Tokens(f.TokensPerSession)))
	}
	body += kv("Recommendation", wrap(f.Recommendation, w-24, 18))
	if f.MonthlySavings >= 0.01 {
		body += kv("Est. monthly", stGood.Render(format.Money(f.MonthlySavings)))
	}
	body += "\n" + stDim.Render("Savings are estimates from recent usage and list prices, not guarantees. TokenManager never edits your files.")
	return lipgloss.JoinVertical(lipgloss.Left, list, panel(f.Title, body, w, h-listH, false))
}

func sevLabel(s optimizer.Severity) string {
	switch s {
	case optimizer.High:
		return stBad.Render("● high")
	case optimizer.Medium:
		return stWarn.Render("● medium")
	case optimizer.Low:
		return stAccent.Render("● low")
	}
	return stDim.Render("● info")
}

// wrap word-wraps s to w cells, indenting continuation lines.
func wrap(s string, w, indent int) string {
	if w < 20 {
		return s
	}
	var lines []string
	line := ""
	for _, word := range strings.Fields(s) {
		if line != "" && len(line)+1+len(word) > w {
			lines = append(lines, line)
			line = word
			continue
		}
		if line != "" {
			line += " "
		}
		line += word
	}
	if line != "" {
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n"+strings.Repeat(" ", indent))
}

// ---------------------------------------------------------------- 8 budgets

func (m *Model) budgetsView(h int) string {
	w := m.w
	bs := m.data.budgets
	if len(bs) == 0 {
		body := "No budgets configured.\n\n" +
			stKey.Render("tokenmanager budget set monthly 100") + "\n" +
			stKey.Render("tokenmanager budget set daily 10 --for agent:claude-code") + "\n" +
			stKey.Render("tokenmanager budget set monthly 40 --for project:my-app") + "\n\n" +
			stDim.Render("Budgets are monitoring only: TokenManager alerts at configurable thresholds\nbut never changes provider-side limits.")
		return panel("Budgets", body, w, h, true)
	}
	var lines []string
	barW := max(10, w-60)
	for i, b := range bs {
		f := b.Fraction()
		st := levelStyle(f)
		name := fit(b.Name(), 30)
		if i == m.cursor[vBudgets] {
			name = stSelected.Render(stripANSI(name))
		}
		lines = append(lines,
			name+" "+st.Render(format.BlockBar(f, barW))+" "+st.Render(padLeft(format.Percent(f), 7)),
			stDim.Render(fmt.Sprintf("%-30s ", "  since "+b.PeriodStart.Format("Jan 2")))+
				fmt.Sprintf("Spent %s   Budget %s   Remaining %s", stBold.Render(format.Money(b.Spent)), format.Money(b.Limit), st.Render(format.Money(b.Remaining()))))
		if b.Projected > 0 {
			pf := b.Projected / b.Limit
			lines = append(lines, stDim.Render(fmt.Sprintf("%-30s ", ""))+"Projected "+levelStyle(pf).Render(format.Money(b.Projected))+stDim.Render(" by end of month"))
		}
		lines = append(lines, "")
	}
	var th []string
	for _, t := range m.cfg.Budgets.Thresholds {
		th = append(th, fmt.Sprintf("%.0f%%", t*100))
	}
	lines = append(lines, stDim.Render("Monitoring only — provider-side limits are not changed. Alerts at "+strings.Join(th, ", ")+" (sent by the daemon)."))
	return panel("Budgets", strings.Join(lines, "\n"), w, h, true)
}

// ---------------------------------------------------------------- 9 settings

func (m *Model) settingsView(h int) string {
	c := m.cfg
	kv := func(k, v string) string { return stDim.Render(fmt.Sprintf("%-20s", k)) + v }
	st, _ := m.db.Stats()
	daemonS := stDim.Render("not running") + stDim.Render("  (tokenmanager daemon start)")
	if m.data.daemonPID > 0 {
		daemonS = stGood.Render(fmt.Sprintf("running (pid %d)", m.data.daemonPID))
	}
	retention := "keep forever"
	if c.RetentionDays > 0 {
		retention = fmt.Sprintf("%d days", c.RetentionDays)
	}
	lines := []string{
		kv("Config file", config.Path()),
		kv("Database", c.DBPath()+stDim.Render(fmt.Sprintf("  (%s requests, %d files tracked)", format.Thousands(st.Records), st.Files))),
		kv("History", fmt.Sprintf("%s → %s", st.First.Local().Format("Jan 2 2006"), st.Last.Local().Format("Jan 2 2006"))),
		kv("Currency", fmt.Sprintf("%s (rate %g from USD)", c.Currency, c.CurrencyRate)),
		kv("Refresh interval", c.RefreshInterval.Duration.String()+stDim.Render(" (live updates use file events)")),
		kv("Retention", retention),
		kv("Notifications", fmt.Sprint(c.Notifications.Enabled)),
		kv("Hide project paths", fmt.Sprint(c.Privacy.HideProjectPaths)),
		kv("Daemon", daemonS),
		kv("Priced models", fmt.Sprintf("%d (%d custom)", len(m.eng.Prices.Models()), len(c.Pricing))),
		"",
		stBold.Render("Agents"),
	}
	for _, s := range m.data.agentSt {
		state := stDim.Render("not found")
		if !s.Enabled {
			state = stDim.Render("disabled")
		} else if s.Installed {
			state = stGood.Render(fmt.Sprintf("detected · %d files", len(s.Files)))
		}
		roots := strings.Join(s.Roots, ", ")
		lines = append(lines, "  "+fit(agentStyle(s.Adapter.Name).Render(s.Adapter.Display), 14)+" "+fit(state, 24)+" "+stDim.Render(roots))
	}
	lines = append(lines, "", stDim.Render("Edit the config file to enable/disable agents, override paths, add prices or budgets.\nPrivacy: only token counts, model names, session ids and project paths are stored. No prompts or code."))
	return panel("Settings", strings.Join(lines, "\n"), m.w, h, true)
}
