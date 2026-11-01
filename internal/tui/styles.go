package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

var (
	colAccent = lipgloss.Color("#A78BFA")
	colCyan   = lipgloss.Color("#67E8F9")
	colGreen  = lipgloss.Color("#4ADE80")
	colYellow = lipgloss.Color("#FBBF24")
	colRed    = lipgloss.Color("#F87171")
	colText   = lipgloss.Color("#E4E4E7")
	colDim    = lipgloss.Color("#71717A")
	colFaint  = lipgloss.Color("#3F3F46")
	colSelBg  = lipgloss.Color("#2E2A47")

	stTitle    = lipgloss.NewStyle().Bold(true).Foreground(colAccent)
	stDim      = lipgloss.NewStyle().Foreground(colDim)
	stText     = lipgloss.NewStyle().Foreground(colText)
	stBold     = lipgloss.NewStyle().Bold(true).Foreground(colText)
	stBig      = lipgloss.NewStyle().Bold(true).Foreground(colText)
	stGood     = lipgloss.NewStyle().Foreground(colGreen)
	stWarn     = lipgloss.NewStyle().Foreground(colYellow)
	stBad      = lipgloss.NewStyle().Foreground(colRed)
	stAccent   = lipgloss.NewStyle().Foreground(colAccent)
	stCyan     = lipgloss.NewStyle().Foreground(colCyan)
	stSelected = lipgloss.NewStyle().Background(colSelBg).Foreground(colText).Bold(true)
	stTabOn    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#0B0B10")).Background(colAccent).Padding(0, 1)
	stTabOff   = lipgloss.NewStyle().Foreground(colDim).Padding(0, 1)
	stLogo     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#0B0B10")).Background(colCyan).Padding(0, 1)
	stKey      = lipgloss.NewStyle().Foreground(colCyan).Bold(true)
)

var agentColors = map[string]lipgloss.Color{
	"claude-code": "#E08A5E",
	"codex":       "#34D399",
	"gemini-cli":  "#60A5FA",
	"aider":       "#F472B6",
	"continue":    "#A3E635",
	"cursor":      "#E4E4E7",
}

func agentStyle(a string) lipgloss.Style {
	if c, ok := agentColors[a]; ok {
		return lipgloss.NewStyle().Foreground(c)
	}
	return stAccent
}

// palette for categorical series (models, providers).
var palette = []lipgloss.Color{"#A78BFA", "#67E8F9", "#F59E0B", "#4ADE80", "#F472B6", "#60A5FA", "#FB7185", "#A3E635"}

func levelStyle(f float64) lipgloss.Style {
	switch {
	case f >= 1:
		return stBad
	case f >= 0.8:
		return stWarn
	}
	return stGood
}

// panel draws a rounded box of exactly w×h cells with a title.
func panel(title, body string, w, h int, focused bool) string {
	if w < 4 || h < 3 {
		return ""
	}
	border := colFaint
	if focused {
		border = colAccent
	}
	innerW, innerH := w-4, h-2
	lines := strings.Split(body, "\n")
	head := stTitle.Render(title)
	if !focused {
		head = stBold.Render(title)
	}
	lines = append([]string{head}, lines...)
	if len(lines) > innerH {
		lines = lines[:innerH]
	}
	for i, l := range lines {
		lines[i] = fit(l, innerW)
	}
	for len(lines) < innerH {
		lines = append(lines, strings.Repeat(" ", innerW))
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).BorderForeground(border).
		Padding(0, 1).Width(w - 2).
		Render(strings.Join(lines, "\n"))
}

// fit truncates or pads a styled line to exactly w cells.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) > w {
		s = ansi.Truncate(s, w, "…")
	}
	if pad := w - lipgloss.Width(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

// padLeft right-aligns s in w cells.
func padLeft(s string, w int) string {
	if pad := w - lipgloss.Width(s); pad > 0 {
		return strings.Repeat(" ", pad) + s
	}
	return s
}
