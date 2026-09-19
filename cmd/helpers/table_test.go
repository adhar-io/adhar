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
