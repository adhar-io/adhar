package helpers

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func TestTableAlignsHeaderRuleAndRows(t *testing.T) {
	// The bug this replaces: `adhar get cluster` separated cells with tabs, so
	// a styled or icon-bearing cell put the header, the rule and the data on
	// three different offsets.
	tb := NewTable("NAME", "STATUS", "ROLES", "AGE").WithBudget(120)
	tb.Row("adhar-control-plane", StateReady("Ready"), IconControlPlane+" control-plane", "13m")
	tb.Row("adhar-worker-1", StateFailed("NotReady"), IconWorker+" worker", "2m")
	lines := strings.Split(tb.Render(), "\n")
	if len(lines) != 4 {
		t.Fatalf("expected header, rule and two rows, got %d lines", len(lines))
	}
	rule := lipgloss.Width(lines[1])
	if rule != tb.Width() {
		t.Errorf("the rule must span the table: %d vs %d", rule, tb.Width())
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w > tb.Width() {
			t.Errorf("line %d exceeds the table width (%d > %d)", i, w, tb.Width())
		}
	}
	// The second column begins at the same cell offset on every line. Offsets
	// are measured with ANSI stripped, since the status cells are coloured.
	offset := func(line, token string) int {
		plain := ansi.Strip(line)
		i := strings.Index(plain, token)
		if i < 0 {
			t.Fatalf("token %q not found in %q", token, plain)
		}
		return lipgloss.Width(plain[:i])
	}
	header := offset(lines[0], "STATUS")
	if r1, r2 := offset(lines[2], IconReady), offset(lines[3], IconFailed); header != r1 || r1 != r2 {
		t.Errorf("columns are not aligned: header=%d row1=%d row2=%d", header, r1, r2)
	}
}

func TestTableFitsTheBudgetByShrinkingTheWidestColumn(t *testing.T) {
	tb := NewTable("NAME", "AGE").WithBudget(30)
	tb.Row(strings.Repeat("x", 80), "10m")
	if tb.Width() > 30 {
		t.Errorf("table must fit its budget, got %d", tb.Width())
	}
	if !strings.Contains(tb.Render(), "...") {
		t.Error("the shrunk column must mark the cut")
	}
	if !strings.Contains(tb.Render(), "10m") {
		t.Error("short columns must survive intact")
	}
}

func TestTableRowToleratesMiscountedCells(t *testing.T) {
	tb := NewTable("A", "B", "C").WithBudget(60)
	tb.Row("only-one")
	tb.Row("one", "two", "three", "ignored")
	out := tb.Render()
	if strings.Contains(out, "ignored") {
		t.Error("extra cells must be ignored rather than shifting the layout")
	}
	if tb.Rows() != 2 {
		t.Errorf("expected 2 rows, got %d", tb.Rows())
	}
}

func TestTruncateDisplayCountsCellsNotBytes(t *testing.T) {
	s := StateReady("Running") // one glyph + space + label, with colour escapes
	if got := TruncateDisplay(s, lipgloss.Width(s)); got != s {
		t.Errorf("a value that fits must be untouched")
	}
	short := TruncateDisplay("a-very-long-name", 10)
	if lipgloss.Width(short) > 10 || !strings.HasSuffix(short, "...") {
		t.Errorf("unexpected %q (width %d)", short, lipgloss.Width(short))
	}
}

func TestStateHelpersAreSingleCellGlyphs(t *testing.T) {
	for name, rendered := range map[string]string{
		"ready": StateReady("x"), "pending": StatePending("x"), "degraded": StateDegraded("x"),
		"failed": StateFailed("x"), "unknown": StateUnknown("x"), "disabled": StateDisabled("x"),
	} {
		if w := lipgloss.Width(rendered); w != 3 {
			t.Errorf("%s renders %d cells for a 1-cell label; icons must be single-cell so tables stay aligned (%q)", name, w, rendered)
		}
	}
}

// A table with several wide columns must still fit. The original reclaimed the
// whole shortfall from the single widest column in one step and gave up entirely
// if that column could not absorb it, so a grid with two or three wide columns
// simply overflowed and every row wrapped — the exact failure Table exists to
// prevent.
func TestTableFitsWhenNoSingleColumnCanAbsorbTheShortfall(t *testing.T) {
	tbl := NewTable("A", "B", "C").WithBudget(60)
	tbl.Row(strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40))
	if got := tbl.Width(); got > 60 {
		t.Errorf("width = %d, want <= 60 (three wide columns must share the shrink)", got)
	}
	for _, line := range strings.Split(tbl.Render(), "\n") {
		if w := lipgloss.Width(line); w > 60 {
			t.Errorf("a rendered line is %d cells wide, over the 60 budget: %q", w, line)
		}
	}
}

// Shrinking takes from whichever column is currently widest, one cell at a time,
// so columns equalise instead of one being gutted while its neighbours keep slack.
func TestTableShrinkEqualisesRatherThanGuttingOneColumn(t *testing.T) {
	// Budget 70: 60+20+20 plus 4 cells of gap is 104, so 34 must go. The wide
	// column can give all of it and still clear the floor, so the narrow ones must
	// be left alone entirely.
	tbl := NewTable("wide", "mid", "mid2").WithBudget(70)
	tbl.Row(strings.Repeat("w", 60), strings.Repeat("m", 20), strings.Repeat("n", 20))
	w, total := tbl.widths()
	if total > 70 {
		t.Fatalf("total = %d, want <= 70", total)
	}
	if w[1] != 20 || w[2] != 20 {
		t.Errorf("the wide column could absorb the shrink alone, so the narrow ones should be untouched: %v", w)
	}
	if w[0] < minColumnWidth {
		t.Errorf("the wide column was squeezed below the floor: %v", w)
	}

	// Under real pressure every column contributes, and they end up within a cell
	// of each other rather than one being gutted.
	tight := NewTable("wide", "mid", "mid2").WithBudget(60)
	tight.Row(strings.Repeat("w", 60), strings.Repeat("m", 20), strings.Repeat("n", 20))
	tw, ttotal := tight.widths()
	if ttotal > 60 {
		t.Errorf("total = %d, want <= 60", ttotal)
	}
	lo, hi := tw[0], tw[0]
	for _, n := range tw {
		if n < lo {
			lo = n
		}
		if n > hi {
			hi = n
		}
	}
	if hi-lo > 1 {
		t.Errorf("columns should equalise under pressure, got %v (spread %d)", tw, hi-lo)
	}
}

// Below the floor a truncated value stops meaning anything, so the table is
// allowed to exceed its budget rather than render a grid of ellipses.
func TestTableStopsShrinkingAtTheReadabilityFloor(t *testing.T) {
	tbl := NewTable("A", "B", "C", "D", "E").WithBudget(20)
	tbl.Row("aaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbb", "cccccccccccccccccccc",
		"dddddddddddddddddddd", "eeeeeeeeeeeeeeeeeeee")
	w, _ := tbl.widths()
	for i, n := range w {
		if n < minColumnWidth {
			t.Errorf("column %d shrank to %d, below the %d floor", i, n, minColumnWidth)
		}
	}
}

// Counts read as a column when their digits line up. "1", "12", "345"
// left-aligned makes the eye re-find the magnitude on every row.
func TestTableRightAlignsNumericColumns(t *testing.T) {
	tbl := NewTable("NAME", "PODS")
	tbl.Row("argocd", "7")
	tbl.Row("gitea", "12")
	tbl.Row("kube-prometheus", "345")

	numeric := tbl.numericColumns()
	if numeric[0] {
		t.Error("a name column must not be right-aligned")
	}
	if !numeric[1] {
		t.Error("a column of counts should be right-aligned")
	}

	lines := strings.Split(tbl.Render(), "\n")
	// header, rule, then three rows
	if len(lines) != 5 {
		t.Fatalf("want 5 lines, got %d", len(lines))
	}
	for _, l := range lines[2:] {
		if !strings.HasSuffix(l, "7") && !strings.HasSuffix(l, "12") && !strings.HasSuffix(l, "345") {
			t.Errorf("numeric cell should end the line with no trailing pad: %q", l)
		}
	}
	// Every row must be the same rendered width, which is what "aligned" means.
	want := lipgloss.Width(lines[2])
	for _, l := range lines[2:] {
		if got := lipgloss.Width(l); got != want {
			t.Errorf("row widths differ: %d vs %d (%q)", got, want, l)
		}
	}
}

// The measurement shapes this CLI actually prints must count as numeric, while an
// identifier that merely starts with a digit must not — it belongs left-aligned
// with its neighbours.
func TestNumericCellRecognisesMeasurementsNotIdentifiers(t *testing.T) {
	for _, v := range []string{"3", "12/75", "99%", "1.5", "256Gi", "4m32s", "10:30", "2026-09-27"} {
		if !isNumericCell(v) {
			t.Errorf("%q should count as numeric", v)
		}
	}
	for _, v := range []string{"3rd-party-thing", "7-day-retention-policy", "adhar", "", "v1.37.0", "-"} {
		if isNumericCell(v) {
			t.Errorf("%q must not count as numeric", v)
		}
	}
}

// A column is classified from its DATA, not its header, and a blank or "-" is a
// gap rather than evidence — one empty cell must not left-align a whole column of
// counts.
func TestNumericColumnsIgnoreGapsAndHeaders(t *testing.T) {
	tbl := NewTable("AGE", "REPLICAS")
	tbl.Row("5m", "3")
	tbl.Row("-", "")
	tbl.Row("2h", "12")
	n := tbl.numericColumns()
	if !n[0] || !n[1] {
		t.Errorf("gaps should not disqualify a numeric column, got %v", n)
	}

	// One genuinely non-numeric value does disqualify it.
	mixed := NewTable("X")
	mixed.Row("3")
	mixed.Row("pending")
	if mixed.numericColumns()[0] {
		t.Error("a column with a word in it must stay left-aligned")
	}
}

// Padding is measured by display width, so a coloured cell lines up with a plain
// one. A byte-based pad drifts by the length of the invisible escape sequence.
func TestTableAlignsStyledCellsWithPlainOnes(t *testing.T) {
	tbl := NewTable("STATE", "NAME")
	tbl.Row(StateReady("Ready"), "argocd")
	tbl.Row("Ready", "gitea")
	lines := strings.Split(tbl.Render(), "\n")[2:]
	if len(lines) != 2 {
		t.Fatalf("want 2 rows, got %d", len(lines))
	}
	// Comparing whole-line widths would be wrong: the final column is never
	// padded, so a shorter last value legitimately makes a shorter line. What
	// must match is where the SECOND column begins — that is what the eye reads
	// as alignment, and what a byte-based pad gets wrong by the length of the
	// invisible escape sequence.
	// Measured in DISPLAY CELLS, not bytes. "●" is three bytes and one cell, so a
	// byte offset reports the styled row as 2 further along than it renders — the
	// very confusion this table exists to avoid.
	offset := func(line, col string) int {
		plain := stripANSI(line)
		i := strings.Index(plain, col)
		if i < 0 {
			t.Fatalf("%q not found in %q", col, plain)
		}
		return lipgloss.Width(plain[:i])
	}
	if a, b := offset(lines[0], "argocd"), offset(lines[1], "gitea"); a != b {
		t.Errorf("second column starts at %d on the styled row but %d on the plain one", a, b)
	}
}
