package get

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestAppStateMapsToTheSharedStateVocabulary(t *testing.T) {
	cases := map[string]string{
		"Ready": "●", "Running": "●", "Healthy": "●",
		"Progressing": "◌", "Pending": "◌",
		"Degraded": "▲",
		"Failed":   "✖", "CrashLoopBackOff": "✖", "NotReady": "✖",
		"Suspended": "○",
		"Weird":     "◍",
	}
	for status, glyph := range cases {
		got := appState(status)
		if !strings.Contains(got, glyph) {
			t.Errorf("appState(%q) should use %q, got %q", status, glyph, got)
		}
		if !strings.Contains(got, status) {
			t.Errorf("appState(%q) must keep the label, got %q", status, got)
		}
		if lipgloss.Width(got) != lipgloss.Width(status)+2 {
			t.Errorf("a state cell must be one glyph plus a space wider than its label: %q is %d cells for a %d-cell label",
				got, lipgloss.Width(got), lipgloss.Width(status))
		}
	}
}

func TestUniqueNamespacesPreservesFirstAppearance(t *testing.T) {
	apps := []ApplicationInfo{{Namespace: "b"}, {Namespace: "a"}, {Namespace: "b"}}
	got := uniqueNamespaces(apps)
	if len(got) != 2 || got[0] != "b" || got[1] != "a" {
		t.Errorf("unexpected %v", got)
	}
}

func TestAppStateStripsAnAlreadyDecoratedStatus(t *testing.T) {
	// The collector emits "✅ Ready" for some workloads; rendering it through the
	// vocabulary produced "● ✅ Ready", two icons in one cell.
	got := appState("✅ Ready")
	if strings.Count(got, "✅") != 0 {
		t.Errorf("the source emoji must be stripped, got %q", got)
	}
	if !strings.Contains(got, "● Ready") {
		t.Errorf("expected a single platform icon plus the label, got %q", got)
	}
	if got := appState("❌ NotReady"); !strings.Contains(got, "✖ NotReady") || strings.Contains(got, "❌") {
		t.Errorf("unexpected %q", got)
	}
}
