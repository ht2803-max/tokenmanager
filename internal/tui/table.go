package tui

import (
	"strings"
)

// col describes one table column. A width of 0 makes the column flexible:
// it takes whatever space the fixed columns leave.
type col struct {
	title string
	width int
	right bool
}

// table renders rows into exactly height lines (header included), keeping
// the cursor row visible and highlighted. cursor < 0 disables selection.
func table(cols []col, rows [][]string, cursor, width, height int) string {
	if height < 2 {
		return ""
	}
	fixed, flex := 0, 0
	for _, c := range cols {
		fixed += c.width + 1
		if c.width == 0 {
			flex++
		}
	}
	widths := make([]int, len(cols))
	for i, c := range cols {
		widths[i] = c.width
		if c.width == 0 {
			widths[i] = max(4, (width-fixed)/max(flex, 1))
		}
	}
	cell := func(s string, i int) string {
		if cols[i].right {
			return padLeft(fit(s, widths[i]), widths[i])
		}
		return fit(s, widths[i])
	}
	line := func(cells []string) string {
		parts := make([]string, len(cols))
		for i := range cols {
			v := ""
			if i < len(cells) {
				v = cells[i]
			}
			if cols[i].right {
				// right-align after trimming padding introduced by fit
				parts[i] = padLeft(strings.TrimRight(fit(v, widths[i]), " "), widths[i])
			} else {
				parts[i] = cell(v, i)
			}
		}
		return strings.Join(parts, " ")
	}
	heads := make([]string, len(cols))
	for i, c := range cols {
		heads[i] = c.title
	}
	out := []string{stDim.Render(fit(line(heads), width))}

	visible := height - 1
	offset := 0
	if cursor >= visible {
		offset = cursor - visible + 1
	}
	for i := offset; i < len(rows) && len(out) < height; i++ {
		l := fit(line(rows[i]), width)
		if i == cursor {
			l = stSelected.Render(stripANSI(l))
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

func stripANSI(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == '\x1b':
			in = true
		case in && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'):
			in = false
		case !in:
			b.WriteRune(r)
		}
	}
	return b.String()
}
