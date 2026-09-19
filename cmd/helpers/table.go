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

// widths sizes each column to its widest cell, then, only if the total exceeds
// the budget, takes the shortfall off the widest column rather than truncating
// every column a little.
func (t *Table) widths() ([]int, int) {
	w := make([]int, len(t.headers))
	for i, h := range t.headers {
		w[i] = lipgloss.Width(h)
	}
	for _, r := range t.rows {
		for i, c := range r {
			if n := lipgloss.Width(c); n > w[i] {
				w[i] = n
			}
		}
	}
	total := 2 * (len(w) - 1)
	for _, n := range w {
		total += n
	}
	if total > t.budget && len(w) > 0 {
		widest, idx := 0, 0
		for i, n := range w {
			if n > widest {
				widest, idx = n, i
			}
		}
		if shrink := total - t.budget; widest-shrink >= 12 {
			w[idx] -= shrink
			total = t.budget
		}
	}
	return w, total
}

// Width is the rendered width in display cells.
func (t *Table) Width() int {
	_, total := t.widths()
	return total
}

// Render returns the header, a full-width rule, and the rows.
func (t *Table) Render() string {
	w, total := t.widths()
	var b strings.Builder
	b.WriteString(lipgloss.NewStyle().Bold(true).Render(renderRow(t.headers, w)))
	b.WriteString("\n" + strings.Repeat("─", total) + "\n")
	for i, r := range t.rows {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(renderRow(r, w))
	}
	return b.String()
}

// renderRow pads each cell to its column width by display width, two spaces
// between columns, and never pads past the last cell.
func renderRow(cells []string, widths []int) string {
	var b strings.Builder
	for i, c := range cells {
		v := TruncateDisplay(c, widths[i])
		b.WriteString(v)
		if i == len(cells)-1 {
			break
		}
		if pad := widths[i] - lipgloss.Width(v); pad > 0 {
			b.WriteString(strings.Repeat(" ", pad))
		}
		b.WriteString("  ")
	}
	return strings.TrimRight(b.String(), " ")
}

// TruncateDisplay shortens s to at most max DISPLAY cells, marking the cut.
// It counts display width, not bytes: a byte-based cut turned "✔ Running"
// (9 cells, 11 bytes) into "✔ R...".
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
