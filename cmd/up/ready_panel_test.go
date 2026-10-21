package up

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// The hint the panel prints for retrieving credentials.
const secretsHint = "adhar get secrets"

// The closing panel must sit flush against the stage checklist.
//
// StageTracker.Finish deliberately prints no trailing gap on success, so that
// the checklist and its result read as one list. A leading newline here undid
// that and split the output in two:
//
//	✓  GitOps sync - platform stack  18m03s
//
//	✓  Platform ready
//
// The local path never had the gap (local.go renders helpers.RenderReadyPanel
// directly), so the cloud path was the inconsistent one.
func TestCloudReadyPanelStartsFlushWithTheChecklist(t *testing.T) {
	out := renderCloudReadyPanel("hub.adhar.io", "adhar")

	if strings.HasPrefix(out, "\n") {
		t.Error("the panel begins with a newline, putting a blank line between the last stage and 'Platform ready'")
	}
	first := out
	if i := strings.IndexByte(out, '\n'); i >= 0 {
		first = out[:i]
	}
	if !strings.Contains(first, "Platform ready") {
		t.Errorf("the panel's first line is %q, expected it to carry 'Platform ready'", first)
	}
	// The panel still has to say what it is for.
	for _, want := range []string{"https://console.hub.adhar.io", secretsHint, "adhar down"} {
		if !strings.Contains(out, want) {
			t.Errorf("the panel no longer mentions %q", want)
		}
	}
}

// The closing commands use the same "Next Steps:" form as `adhar down` and the
// setup summary — a heading, then "→ Run <command> to <purpose>" — on the
// shared gutter, instead of a bare label/command table (2026-10-21).
func TestReadyPanelEndsWithNextSteps(t *testing.T) {
	out := ansi.Strip(renderCloudReadyPanel("hub.adhar.io", "adhar"))
	i := strings.Index(out, "\n  Next Steps:\n")
	if i < 0 {
		t.Fatalf("no 'Next Steps:' heading on the gutter:\n%s", out)
	}
	steps := strings.Split(strings.TrimRight(out[i+len("\n  Next Steps:\n"):], "\n"), "\n")
	if len(steps) < 3 {
		t.Fatalf("expected the command list under the heading, got %q", steps)
	}
	for _, l := range steps {
		if !strings.HasPrefix(l, "  → Run ") || !strings.Contains(l, " to ") {
			t.Errorf("step %q is not in the '→ Run <command> to <purpose>' form", l)
		}
	}
}
