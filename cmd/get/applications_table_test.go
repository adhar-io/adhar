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
