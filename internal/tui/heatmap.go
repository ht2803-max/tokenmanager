package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/ht2803-max/tokenmanager/internal/analytics"
	"github.com/ht2803-max/tokenmanager/internal/format"
)

// Heatmap panel heights: full (title, month row, seven weekday rows, legend,
// border) down to the minimum (title, seven rows, border).
const (
	heatmapH    = 12
	heatmapMinH = 10
	// heatFit is the Overview body height the heatmap needs below the cards:
	// the smallest heatmap in the chart slot plus a usable sessions panel.
	heatFit = heatmapMinH + 6
)

// heatLevels colors heatmap squares from no usage (0) to the busiest days (4).
var heatLevels = [5]lipgloss.Style{
	lipgloss.NewStyle().Foreground(colFaint),
	lipgloss.NewStyle().Foreground(lipgloss.Color("#4C3D80")),
	lipgloss.NewStyle().Foreground(lipgloss.Color("#6D5BC4")),
	lipgloss.NewStyle().Foreground(colAccent),
	lipgloss.NewStyle().Foreground(lipgloss.Color("#DDD6FE")),
}

const (
	heatCell   = "■"
	heatLabelW = 4 // "Mon "
)

// heatLayout picks the cell width (2 = square plus gap, 1 = dense) and how
// many recent weeks fit in an inner width of w cells.
func heatLayout(a analytics.Activity, w int) (cellW, weeks int) {
	total := (len(a.Days) + 6) / 7
	cellW = 2
	if (w-heatLabelW)/2 < total {
		cellW = 1
	}
	return cellW, min(total, max(1, (w-heatLabelW)/cellW))
}

// heatPanel renders the activity heatmap as a w×h panel (h >= heatmapMinH).
func heatPanel(a analytics.Activity, w, h int, focused bool) string {
	_, weeks := heatLayout(a, w-4)
	return panel(heatmapTitle(a, weeks), heatmap(a, w-4, h-3), w, h, focused)
}

// shownDays is the slice of days drawn when only the last `weeks` weeks fit.
func shownDays(a analytics.Activity, weeks int) []analytics.ActivityDay {
	total := (len(a.Days) + 6) / 7
	return a.Days[(total-weeks)*7:]
}

func heatmapTitle(a analytics.Activity, weeks int) string {
	span := "last 12 months"
	if weeks < 52 {
		span = fmt.Sprintf("last %d weeks", weeks)
	}
	var tokens int64
	active := 0
	for _, d := range shownDays(a, weeks) {
		tokens += d.Tokens
		if d.Tokens > 0 {
			active++
		}
	}
	return fmt.Sprintf("Activity · %s · %s tokens · %d active days", span, format.Tokens(tokens), active)
}

// heatmap renders a GitHub-style calendar in w×h cells: one column per week
// (Monday on top), as many recent weeks as fit. With fewer than 9 rows it
// drops the legend, then the month labels; it always draws the 7 weekdays.
func heatmap(a analytics.Activity, w, h int) string {
	cellW, weeks := heatLayout(a, w)
	days := shownDays(a, weeks)
	var lines []string

	if h >= 8 {
		lines = append(lines, stDim.Render(monthRow(days, weeks, cellW, w)))
	}
	dayLabels := [7]string{"Mon", "", "Wed", "", "Fri", "", ""}
	gap := strings.Repeat(" ", cellW-1)
	for r := 0; r < 7; r++ {
		var b strings.Builder
		b.WriteString(stDim.Render(fmt.Sprintf("%-*s", heatLabelW, dayLabels[r])))
		for c := 0; c < weeks; c++ {
			i := c*7 + r
			if i >= len(days) {
				break // later this week
			}
			b.WriteString(heatLevels[a.Level(days[i].Tokens)].Render(heatCell))
			b.WriteString(gap)
		}
		lines = append(lines, b.String())
	}
	if h >= 9 {
		lines = append(lines, heatLegend(a, weeks))
	}
	for i, l := range lines {
		lines[i] = fit(l, w)
	}
	return strings.Join(lines, "\n")
}

// monthRow places a month name above the first week of each month,
// skipping labels that would overlap the previous one.
func monthRow(days []analytics.ActivityDay, weeks, cellW, w int) string {
	type mark struct {
		col   int
		label string
	}
	var marks []mark
	for c := 0; c < weeks && c*7 < len(days); c++ {
		d := days[c*7].Date
		if c == 0 || d.Month() != days[c*7-7].Date.Month() {
			marks = append(marks, mark{c, d.Format("Jan")})
		}
	}
	minGap := 4 / cellW // columns a 3-letter label plus a space needs
	if len(marks) > 1 && marks[1].col < minGap {
		marks = marks[1:] // a partial first month would collide with the next label
	}
	row := []rune(strings.Repeat(" ", max(w, heatLabelW+weeks*cellW+3)))
	end := 0
	for _, m := range marks {
		pos := heatLabelW + m.col*cellW
		if pos < end {
			continue
		}
		copy(row[pos:], []rune(m.label))
		end = pos + len(m.label) + 1
	}
	return strings.TrimRight(string(row), " ")
}

func heatLegend(a analytics.Activity, weeks int) string {
	legend := stDim.Render("Less ")
	for _, s := range heatLevels {
		legend += s.Render(heatCell) + " "
	}
	legend += stDim.Render("More")
	switch {
	case a.ActiveDays == 0:
		legend += "   " + stDim.Render("No usage in the last 12 months.")
	case weeks < 52:
		legend += "   " + stDim.Render(fmt.Sprintf("streak %dd", a.Streak))
	default:
		legend += "   " + stDim.Render(fmt.Sprintf("streak %dd · longest %dd · peak %s (%s)",
			a.Streak, a.LongestStreak, a.Peak.Date.Format("Jan 2"), format.Tokens(a.Peak.Tokens)))
	}
	return legend
}
