// Package format holds number formatting and Unicode chart primitives shared
// by the CLI output and the TUI.
package format

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// Currency is the active currency code, set from config at startup.
var Currency = "USD"

var symbols = map[string]string{"USD": "$", "EUR": "€", "GBP": "£", "JPY": "¥", "INR": "₹", "CAD": "C$", "AUD": "A$", "CHF": "CHF "}

// Money formats an amount in the active currency.
func Money(v float64) string {
	sym, ok := symbols[strings.ToUpper(Currency)]
	if !ok {
		sym = Currency + " "
	}
	if v < 0 {
		return "-" + Money(-v)
	}
	if v >= 10000 {
		return sym + Thousands(int64(math.Round(v)))
	}
	return fmt.Sprintf("%s%.2f", sym, v)
}

// Tokens formats a token count compactly (12.3k, 4.5M).
func Tokens(n int64) string {
	f := float64(n)
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.2fB", f/1e9)
	case n >= 10_000_000:
		return fmt.Sprintf("%.1fM", f/1e6)
	case n >= 1_000_000:
		return fmt.Sprintf("%.2fM", f/1e6)
	case n >= 10_000:
		return fmt.Sprintf("%.0fk", f/1e3)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", f/1e3)
	}
	return fmt.Sprintf("%d", n)
}

// Thousands formats with comma separators.
func Thousands(n int64) string {
	s := fmt.Sprintf("%d", n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// Percent formats a fraction.
func Percent(f float64) string { return fmt.Sprintf("%.1f%%", f*100) }

// Duration formats a session duration.
func Duration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd%02dh", int(d.Hours())/24, int(d.Hours())%24)
}

// Ago formats a relative time.
func Ago(t time.Time, now time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
	return t.Local().Format("Jan 2")
}

// Bar renders a horizontal progress bar of width cells for fraction f,
// using eighth-blocks for sub-cell precision.
func Bar(f float64, width int) string {
	if width <= 0 {
		return ""
	}
	f = math.Max(0, math.Min(1, f))
	eighths := int(math.Round(f * float64(width*8)))
	full := eighths / 8
	rem := eighths % 8
	parts := []string{"", "▏", "▎", "▍", "▌", "▋", "▊", "▉"}
	s := strings.Repeat("█", full)
	if full < width {
		s += parts[rem]
		pad := width - full - 1
		if rem == 0 {
			pad = width - full
		}
		s += strings.Repeat(" ", pad)
	}
	return s
}

// BlockBar renders a bar using █ and ░ like "████████░░░░░░░░".
func BlockBar(f float64, width int) string {
	f = math.Max(0, math.Min(1, f))
	n := int(math.Round(f * float64(width)))
	return strings.Repeat("█", n) + strings.Repeat("░", width-n)
}

var sparks = []rune("▁▂▃▄▅▆▇█")

// Sparkline renders values as a one-line chart.
func Sparkline(vals []float64) string {
	hi := 0.0
	for _, v := range vals {
		hi = math.Max(hi, v)
	}
	var b strings.Builder
	for _, v := range vals {
		if hi == 0 || v <= 0 {
			b.WriteRune(' ')
			continue
		}
		i := int(math.Round(v / hi * float64(len(sparks)-1)))
		b.WriteRune(sparks[i])
	}
	return b.String()
}

// Columns renders a multi-row vertical bar chart (height rows) for vals.
func Columns(vals []float64, height int) []string {
	hi := 0.0
	for _, v := range vals {
		hi = math.Max(hi, v)
	}
	rows := make([]string, height)
	for row := 0; row < height; row++ {
		var b strings.Builder
		level := height - row // 1-based row from bottom
		for _, v := range vals {
			if hi == 0 {
				b.WriteRune(' ')
				continue
			}
			units := v / hi * float64(height*8)
			cell := units - float64((level-1)*8)
			switch {
			case cell >= 8:
				b.WriteRune('█')
			case cell <= 0:
				if level == 1 && v > 0 {
					b.WriteRune('▁')
				} else {
					b.WriteRune(' ')
				}
			default:
				b.WriteRune(sparks[max(0, int(math.Ceil(cell))-1)])
			}
		}
		rows[row] = b.String()
	}
	return rows
}

// Truncate shortens s to n display cells with an ellipsis.
func Truncate(s string, n int) string {
	r := []rune(s)
	if n <= 0 {
		return ""
	}
	if len(r) <= n {
		return s
	}
	if n == 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}

// TruncateLeft keeps the end of s (useful for paths).
func TruncateLeft(s string, n int) string {
	r := []rune(s)
	if len(r) <= n || n <= 1 {
		return s
	}
	return "…" + string(r[len(r)-n+1:])
}
