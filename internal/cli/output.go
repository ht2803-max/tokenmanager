package cli

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/tokenmanager/tokenmanager/internal/format"
)

var (
	cAccent = lipgloss.AdaptiveColor{Light: "#5A3FD9", Dark: "#A78BFA"}
	cGood   = lipgloss.AdaptiveColor{Light: "#15803D", Dark: "#4ADE80"}
	cWarn   = lipgloss.AdaptiveColor{Light: "#B45309", Dark: "#FBBF24"}
	cBad    = lipgloss.AdaptiveColor{Light: "#B91C1C", Dark: "#F87171"}
	cDim    = lipgloss.AdaptiveColor{Light: "#6B7280", Dark: "#6B7280"}

	sTitle  = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	sHead   = lipgloss.NewStyle().Bold(true).Foreground(cDim)
	sDim    = lipgloss.NewStyle().Foreground(cDim)
	sBold   = lipgloss.NewStyle().Bold(true)
	sGood   = lipgloss.NewStyle().Foreground(cGood)
	sWarn   = lipgloss.NewStyle().Foreground(cWarn)
	sBad    = lipgloss.NewStyle().Foreground(cBad)
	sAccent = lipgloss.NewStyle().Foreground(cAccent)
)

// table is a simple aligned text table.
type table struct {
	headers []string
	right   []bool
	rows    [][]string
}

func newTable(headers ...string) *table {
	t := &table{headers: headers, right: make([]bool, len(headers))}
	return t
}

// alignRight marks columns (by index) as right-aligned.
func (t *table) alignRight(cols ...int) *table {
	for _, c := range cols {
		t.right[c] = true
	}
	return t
}

func (t *table) add(cells ...string) { t.rows = append(t.rows, cells) }

func (t *table) render(w io.Writer) {
	widths := make([]int, len(t.headers))
	for i, h := range t.headers {
		widths[i] = lipgloss.Width(h)
	}
	for _, r := range t.rows {
		for i, c := range r {
			widths[i] = max(widths[i], lipgloss.Width(c))
		}
	}
	line := func(cells []string, style *lipgloss.Style) {
		var b strings.Builder
		for i, c := range cells {
			pad := widths[i] - lipgloss.Width(c)
			if style != nil {
				c = style.Render(c)
			}
			if t.right[i] {
				b.WriteString(strings.Repeat(" ", pad) + c)
			} else {
				b.WriteString(c + strings.Repeat(" ", pad))
			}
			if i < len(cells)-1 {
				b.WriteString("  ")
			}
		}
		fmt.Fprintln(w, "  "+strings.TrimRight(b.String(), " "))
	}
	line(t.headers, &sHead)
	for _, r := range t.rows {
		line(r, nil)
	}
}

func (t *table) csv(w io.Writer) error {
	cw := csv.NewWriter(w)
	_ = cw.Write(t.headers)
	for _, r := range t.rows {
		_ = cw.Write(r)
	}
	cw.Flush()
	return cw.Error()
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func heading(w io.Writer, title, sub string) {
	fmt.Fprintln(w)
	if sub != "" {
		fmt.Fprintf(w, "  %s  %s\n", sTitle.Render(strings.ToUpper(title)), sDim.Render(sub))
	} else {
		fmt.Fprintf(w, "  %s\n", sTitle.Render(strings.ToUpper(title)))
	}
}

// usageColor picks green/yellow/red for a budget fraction.
func usageStyle(f float64) lipgloss.Style {
	switch {
	case f >= 1:
		return sBad
	case f >= 0.8:
		return sWarn
	}
	return sGood
}

// costCell renders a cost, marking partially unpriced totals.
func costCell(cost float64, unpriced int64) string {
	if unpriced > 0 && cost == 0 {
		return "n/a"
	}
	s := format.Money(cost)
	if unpriced > 0 {
		s += "*"
	}
	return s
}
