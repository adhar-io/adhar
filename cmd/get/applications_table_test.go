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
	// Upstream collectors decorate some statuses themselves, with whatever symbol
	// they happen to use. Rendering one through the shared vocabulary then produced
	// two icons in one cell ("● ✅ Ready").
	//
	// Stripping is by character class rather than a list of known glyphs: the list
	// this replaced had to enumerate every symbol any source might send, and a bulk
	// edit of the CLI's icons mangled it into duplicates — after which appState
	// could no longer strip the emoji it exists to strip, and no test noticed
	// because the fixtures had been rewritten by the same edit.
	for _, decorated := range []string{
		"✅ Ready", "● Ready", "✔ Ready", "🟢 Ready", "  ✅  Ready", "[✅] Ready",
	} {
		got := appState(decorated)
		if strings.Count(got, "●") != 1 {
			t.Errorf("appState(%q) = %q, want exactly one vocabulary icon", decorated, got)
		}
		if !strings.Contains(got, "Ready") {
			t.Errorf("appState(%q) = %q, lost the label", decorated, got)
		}
		for _, stray := range []string{"✅", "✔", "🟢"} {
			if strings.Contains(got, stray) {
				t.Errorf("appState(%q) = %q, still carries %q", decorated, got, stray)
			}
		}
	}

	// A failure state keeps its own icon and must not be read as ready —
	// "NotReady" contains "ready".
	got := appState("❌ NotReady")
	if !strings.Contains(got, "NotReady") || strings.Contains(got, "❌") {
		t.Errorf("appState(\"❌ NotReady\") = %q", got)
	}
	if strings.Count(got, "✖") != 1 {
		t.Errorf("a failed state should render one failure icon, got %q", got)
	}

	// A digit-leading label must survive: "2/3 Ready" is a real status.
	if got := appState("2/3 Ready"); !strings.Contains(got, "2/3 Ready") {
		t.Errorf("a numeric label must not be eaten as decoration, got %q", got)
	}
}

// Kubernetes' and Kind's own namespaces hold no Adhar application, so they are
// hidden from `adhar get apps`. kube-system and local-path-storage in particular
// cannot be emptied instead: kube-system holds the static control-plane pods, and
// Kind creates local-path-storage before adhar-system exists.
func TestHideInfraNamespacesDropsOnlyInfrastructureRows(t *testing.T) {
	apps := []ApplicationInfo{
		{Name: "adhar-console", Namespace: "adhar-system"},
		{Name: "coredns", Namespace: "kube-system"},
		{Name: "local-path-provisioner", Namespace: "local-path-storage"},
		{Name: "kpack-controller", Namespace: "kpack-system"},
		{Name: "my-api", Namespace: "team-payments"},
	}

	got := hideInfraNamespaces(apps, "", false)

	var names []string
	for _, a := range got {
		names = append(names, a.Name)
	}
	if len(got) != 2 {
		t.Fatalf("expected the two application rows to survive, got %v", names)
	}
	if names[0] != "adhar-console" || names[1] != "my-api" {
		t.Errorf("wrong rows kept: %v", names)
	}
}

// Hiding a namespace must never override an explicit request for it: a user who
// types -n kube-system is asking to see exactly that.
func TestHideInfraNamespacesRespectsAnExplicitRequest(t *testing.T) {
	apps := []ApplicationInfo{{Name: "coredns", Namespace: "kube-system"}}

	if got := hideInfraNamespaces(apps, "kube-system", false); len(got) != 1 {
		t.Errorf("-n kube-system must still list kube-system, got %d rows", len(got))
	}
	if got := hideInfraNamespaces(apps, "", true); len(got) != 1 {
		t.Errorf("--include-system must list every namespace, got %d rows", len(got))
	}
}

// An application namespace that merely resembles an infrastructure one is a
// normal namespace and must be listed.
func TestHideInfraNamespacesMatchesWholeNamesOnly(t *testing.T) {
	apps := []ApplicationInfo{
		{Name: "a", Namespace: "kube-system-tools"},
		{Name: "b", Namespace: "my-kube-system"},
	}
	if got := hideInfraNamespaces(apps, "", false); len(got) != 2 {
		t.Errorf("only exact namespace names may be hidden, got %d rows", len(got))
	}
}
