/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package helpers

import (
	"os"
	"strings"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
)

// Table is the CLI's one column-aligned table.
//
// Every column is sized to its widest cell, measured in DISPLAY cells with
// lipgloss so colour escapes and wide glyphs still line up, and the whole table
// is fitted to the terminal. The two things it exists to prevent, both of which
// shipped:
//
//   - Tab-separated rows (`adhar get cluster`): tabs snap to 8-column stops, so
//     a styled or icon-bearing cell pushed the header, the separator and the
//     data onto three different offsets.
//   - Fixed `%-30s` widths (`adhar get apps`): the columns added up to more
//     cells than the surrounding border, so every row wrapped onto a second
//     line, and byte-counted padding misaligned any cell containing an icon.
type Table struct {
	headers []string
	rows    [][]string
	budget  int
}

// NewTable starts a table with the given column headers.
func NewTable(headers ...string) *Table {
	return &Table{headers: headers, budget: TerminalBudget()}
}

// WithBudget overrides the width the table may occupy (tests, nested boxes).
func (t *Table) WithBudget(cells int) *Table {
	t.budget = cells
	return t
}

// Row appends a row. Extra cells are ignored and missing cells are blank, so a
// caller can never break the layout by miscounting.
func (t *Table) Row(cells ...string) *Table {
	row := make([]string, len(t.headers))
	for i := range row {
		if i < len(cells) {
			row[i] = cells[i]
		}
	}
	t.rows = append(t.rows, row)
	return t
}

// Rows reports how many data rows the table holds.
func (t *Table) Rows() int { return len(t.rows) }

// minColumnWidth is the narrowest a column may be squeezed to. Below about this
// much even a truncated value ("adhar-…") stops carrying information, so it is
// better to let the table exceed its budget — and say so by overflowing — than to
// render a grid of ellipses.
const minColumnWidth = 12

// widths sizes each column to its widest cell, then, if the total exceeds the
// budget, reclaims the shortfall from the widest columns first.
//
// Reclaiming REPEATEDLY from whichever column is currently widest is what makes
// the grid fit. The original took the whole shortfall off the single widest
// column in one go, and gave up entirely if that one column could not absorb it
// — so a table with two or three wide columns simply overflowed and every row
// wrapped, which is the failure this type exists to prevent. Shaving the widest
// column one step at a time also equalises them naturally: a 60/40/40 split
// under pressure becomes 47/40/40 rather than 20/40/40, so no single column is
// gutted while its neighbours keep slack they are not using.
func (t *Table) widths() ([]int, int) {
	w := make([]int, len(t.headers))
	for i, h := range t.headers {
		w[i] = lipgloss.Width(h)
	}
	for _, r := range t.rows {
		for i, c := range r {
			if i < len(w) {
				if n := lipgloss.Width(c); n > w[i] {
					w[i] = n
				}
			}
		}
	}

	gaps := 0
	if len(w) > 1 {
		gaps = 2 * (len(w) - 1)
	}
	total := gaps
	for _, n := range w {
		total += n
	}
	if len(w) == 0 {
		return w, total
	}

	// Shave the widest column by one cell at a time until it fits or every column
	// has reached the floor. Bounded by the overshoot, so this cannot spin.
	for total > t.budget {
		widest, idx := 0, -1
		for i, n := range w {
			if n > widest && n > minColumnWidth {
				widest, idx = n, i
			}
		}
		if idx < 0 {
			break // nothing left to give without making the table unreadable
		}
		w[idx]--
		total--
	}
	return w, total
}

// numericColumns reports which columns hold only numbers, so they can be
// right-aligned. A column of counts reads as a column when the digits line up and
// as noise when they do not — "1", "12", "345" left-aligned forces the eye to
// re-find the magnitude on every row.
//
// The header is deliberately NOT considered: it is a word ("PODS", "AGE") and
// would never look numeric. An empty or "-" cell is treated as a gap rather than
// as evidence either way, so one blank does not left-align a whole column.
func (t *Table) numericColumns() []bool {
	numeric := make([]bool, len(t.headers))
	seen := make([]bool, len(t.headers))
	for _, r := range t.rows {
		for i, c := range r {
			if i >= len(numeric) {
				continue
			}
			v := strings.TrimSpace(stripANSI(c))
			if v == "" || v == "-" || v == "—" {
				continue
			}
			if !isNumericCell(v) {
				numeric[i] = false
				seen[i] = true
				continue
			}
			if !seen[i] {
				numeric[i] = true
				seen[i] = true
			}
		}
	}
	for i := range numeric {
		if !seen[i] {
			numeric[i] = false
		}
	}
	return numeric
}

// isNumericCell accepts the shapes a count or measurement actually takes in this
// CLI — "3", "12/75", "99%", "1.5", "256Gi", "4m32s" — but not identifiers that
// merely begin with a digit, which must stay left-aligned with their neighbours.
func isNumericCell(v string) bool {
	if v == "" {
		return false
	}
	if !unicode.IsDigit(rune(v[0])) {
		return false
	}
	letters := 0
	for _, r := range v {
		switch {
		case unicode.IsDigit(r), r == '.', r == '/', r == '%', r == ':', r == '-', r == '+':
		case unicode.IsLetter(r):
			letters++
		default:
			return false
		}
	}
	// A short unit suffix is fine (Gi, MiB, ms, s, m, h, d); a word is not.
	return letters <= 3
}

// stripANSI removes escape sequences so a styled cell can be inspected for
// content. lipgloss.Width already ignores them for measurement, but the value
// itself has to be read to classify a column.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			i++
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// Width is the rendered width in display cells.
func (t *Table) Width() int {
	_, total := t.widths()
	return total
}

// Header and rule styling. Muted rather than loud: the rows carry the colour
// that means something (the State* vocabulary), so the furniture around them
// should recede. A bright header competes with a red failure two lines below it.
var (
	tableHeaderStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#94a3b8"))
	tableRuleStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#334155"))
)

// Render returns the header, a full-width rule, and the rows.
//
// Numeric columns are right-aligned so digits line up; everything else is
// left-aligned. The header follows its column's alignment, so a right-aligned
// count sits under its own label rather than drifting away from it.
func (t *Table) Render() string {
	w, total := t.widths()
	numeric := t.numericColumns()

	var b strings.Builder
	b.WriteString(tableHeaderStyle.Render(renderRow(t.headers, w, numeric)))
	b.WriteString("\n" + tableRuleStyle.Render(strings.Repeat("─", total)) + "\n")
	for i, r := range t.rows {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(renderRow(r, w, numeric))
	}
	return b.String()
}

// renderRow pads each cell to its column width by DISPLAY width, two spaces
// between columns, and never pads past the last cell.
//
// `numeric` marks the columns to right-align; it may be shorter than the row (or
// nil) and any column it does not cover is left-aligned. Padding is measured with
// lipgloss.Width so a styled cell — which carries invisible escape bytes — lines
// up with a plain one.
func renderRow(cells []string, widths []int, numeric []bool) string {
	var b strings.Builder
	for i, c := range cells {
		if i >= len(widths) {
			break
		}
		v := TruncateDisplay(c, widths[i])
		pad := widths[i] - lipgloss.Width(v)
		if pad < 0 {
			pad = 0
		}
		right := i < len(numeric) && numeric[i]

		// A right-aligned cell is padded BEFORE the value, including in the last
		// column — otherwise the final column of counts would not align at all.
		if right && pad > 0 {
			b.WriteString(strings.Repeat(" ", pad))
		}
		b.WriteString(v)
		if i == len(cells)-1 {
			break
		}
		if !right && pad > 0 {
			b.WriteString(strings.Repeat(" ", pad))
		}
		b.WriteString("  ")
	}
	// Trailing spaces from a left-aligned final column are noise; a right-aligned
	// one has none to trim.
	return strings.TrimRight(b.String(), " ")
}

// TruncateDisplay shortens s to at most max DISPLAY cells, marking the cut.
// It counts display width, not bytes: a byte-based cut turned "● Running"
// (9 cells, 11 bytes) into "● R...".
func TruncateDisplay(s string, max int) string {
	if max <= 0 || lipgloss.Width(s) <= max {
		return s
	}
	runes := []rune(s)
	if max <= 3 {
		return string(runes[:max])
	}
	for n := len(runes); n > 0; n-- {
		if candidate := string(runes[:n]); lipgloss.Width(candidate)+3 <= max {
			return candidate + "..."
		}
	}
	return "..."
}

// TerminalBudget is how many display cells a table may use: the terminal width
// less the surrounding border and padding, clamped so it is never uselessly
// narrow nor absurdly wide.
func TerminalBudget() int {
	budget := 110
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		budget = w - 6
	}
	if budget < 60 {
		budget = 60
	}
	if budget > 200 {
		budget = 200
	}
	return budget
}
