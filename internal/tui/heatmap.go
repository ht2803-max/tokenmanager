package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/ht2803-max/tokenmanager/internal/analytics"
	"github.com/ht2803-max/tokenmanager/internal/format"
)

// heatmapH is the Overview activity panel's height: title, month row, seven
// weekday rows and the legend, plus the border.
const heatmapH = 12

// heatLevels colors heatmap squares from no usage (0) to the busiest days (4).
var heatLevels = [5]lipgloss.Style{
	lipgloss.NewStyle().Foreground(colFaint),
	lipgloss.NewStyle().Foreground(lipgloss.Color("#4C3D80")),
	lipgloss.NewStyle().Foreground(lipgloss.Color("#6D5BC4")),
	lipgloss.NewStyle().Foreground(colAccent),
	lipgloss.NewStyle().Foreground(lipgloss.Color("#DDD6FE")),
}

const heatCell = "■"

func heatmapTitle(a analytics.Activity) string {
	return fmt.Sprintf("Activity · last 12 months · %s tokens · %d active days", format.Tokens(a.Total), a.ActiveDays)
}

// heatmap renders a GitHub-style calendar: one column per week (Monday on
// top), as many recent weeks as fit in w cells.
func heatmap(a analytics.Activity, w int) string {
	const labelW = 4
	total := (len(a.Days) + 6) / 7
	weeks := min(total, max(1, (w-labelW)/2))
	offset := (total - weeks) * 7

	// Month labels above the first week of each month, without overlaps.
	type mark struct {
		col   int
		label string
	}
	var marks []mark
	for c := 0; c < weeks; c++ {
		i := offset + c*7
		if i >= len(a.Days) {
			break
		}
		d := a.Days[i].Date
		if c == 0 || d.Month() != a.Days[i-7].Date.Month() {
			marks = append(marks, mark{c, d.Format("Jan")})
		}
	}
	if len(marks) > 1 && marks[1].col < 2 {
		marks = marks[1:] // a partial first month would collide with the next label
	}
	months := []rune(strings.Repeat(" ", max(w, labelW+weeks*2+3)))
	end := 0
	for _, m := range marks {
		pos := labelW + m.col*2
		if pos < end {
			continue
		}
		copy(months[pos:], []rune(m.label))
		end = pos + len(m.label) + 1
	}
	lines := []string{stDim.Render(strings.TrimRight(string(months), " "))}

	dayLabels := [7]string{"Mon", "", "Wed", "", "Fri", "", ""}
	for r := 0; r < 7; r++ {
		var b strings.Builder
		b.WriteString(stDim.Render(fmt.Sprintf("%-*s", labelW, dayLabels[r])))
		for c := 0; c < weeks; c++ {
			i := offset + c*7 + r
			if i >= len(a.Days) {
				break // later this week
			}
			b.WriteString(heatLevels[a.Level(a.Days[i].Tokens)].Render(heatCell))
			b.WriteByte(' ')
		}
		lines = append(lines, b.String())
	}

	legend := stDim.Render("Less ")
	for _, s := range heatLevels {
		legend += s.Render(heatCell) + " "
	}
	legend += stDim.Render("More")
	if a.ActiveDays == 0 {
		legend += "   " + stDim.Render("No usage in the last 12 months.")
	} else {
		legend += "   " + stDim.Render(fmt.Sprintf("streak %dd · longest %dd · peak %s (%s)",
			a.Streak, a.LongestStreak, a.Peak.Date.Format("Jan 2"), format.Tokens(a.Peak.Tokens)))
	}
	lines = append(lines, legend)
	for i, l := range lines {
		lines[i] = fit(l, w)
	}
	return strings.Join(lines, "\n")
}
