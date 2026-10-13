package cost

import (
	"strings"
	"testing"
)

// A column of money must be readable DOWN, which means one precision for all
// of it.
//
// `money` chooses per value: $491 for one namespace and $11.43 for the next,
// in the same column, which lines up no digit with any other and reads as two
// different units. Live output on 2026-10-14:
//
//	NAMESPACE     $/HOUR   $/MONTH (PROJ.)
//	adhar-system  $0.6729  $491
//	kube-system   $0.0157  $11.43
func TestAMoneyColumnUsesOnePrecisionForEveryRow(t *testing.T) {
	f := columnMoney(491)
	big, small := f(491), f(11.43)
	if decimals(big) != decimals(small) {
		t.Errorf("one column printed %q and %q — a column is compared down, so every row must "+
			"carry the same number of decimals", big, small)
	}

	// And a column whose largest value is small keeps the precision that
	// column needs, or every hourly figure collapses to $0.
	h := columnMoney(0.6886)
	if h(0.0157) == "$0" || decimals(h(0.0157)) != decimals(h(0.6886)) {
		t.Errorf("hourly column rendered %q / %q; sub-dollar columns need their decimals",
			h(0.0157), h(0.6886))
	}
}

func decimals(s string) int {
	i := strings.IndexByte(s, '.')
	if i < 0 {
		return 0
	}
	return len(s) - i - 1
}

// With no budget declared — the normal state, and the one the command prints
// advice about — BUDGET and USED are two columns of "—" taking a third of the
// width to say nothing.
func TestTheBudgetColumnsAreAbsentUntilABudgetExists(t *testing.T) {
	without := strings.Join(costHeaders(false), " ")
	if strings.Contains(without, "BUDGET") || strings.Contains(without, "USED") {
		t.Errorf("no budget is declared yet the table still reserves budget columns: %q", without)
	}
	with := strings.Join(costHeaders(true), " ")
	for _, want := range []string{"BUDGET", "USED"} {
		if !strings.Contains(with, want) {
			t.Errorf("a declared budget must still be shown; %q is missing from %q", want, with)
		}
	}
}

// The share column is what makes the table answer "which namespace do I look
// at" — one row being nearly the whole bill is the first thing to know.
func TestTheShareColumnReportsEachNamespacesProportion(t *testing.T) {
	got := shareBar(0.6729, 0.6886)
	if !strings.Contains(got, "97.7%") {
		t.Errorf("share of 0.6729/0.6886 rendered %q, want it to state 97.7%%", got)
	}
	if !strings.Contains(got, "▰") {
		t.Errorf("share %q carries no bar; the proportion is the point", got)
	}
	if shareBar(1, 0) != "" {
		t.Error("a zero total must render an empty share rather than dividing by zero")
	}
	// The bar must not overflow its width when a single namespace is everything.
	full := shareBar(1, 1)
	if strings.Count(full, "▰") != 10 {
		t.Errorf("a 100%% share drew %d filled cells, want 10: %q", strings.Count(full, "▰"), full)
	}
}
