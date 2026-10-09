package up

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The apps-budget watchdog runs while the live checklist owns the terminal.
// Anything it writes straight to stderr lands between the tracker's frames,
// desynchronises the cursor arithmetic and leaves an orphaned copy of the whole
// block on screen — two "Provisioning Adhar Platform" headers, one truncated,
// 15s apart (AWS bring-up, 2026-10-09, right as the 15-minute budget elapsed).
// The logger is routed through the tracker on both the local and cloud paths,
// so that is the only way out.
func TestTheAppsBudgetWatchdogNeverWritesToTheTerminalDirectly(t *testing.T) {
	src, err := os.ReadFile("local.go")
	if err != nil {
		t.Fatal(err)
	}
	body := functionBody(t, string(src), "watchAppsBudget")
	for _, forbidden := range []string{"os.Stderr", "os.Stdout", "fmt.Print"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("watchAppsBudget writes to the terminal directly (%s); use the logger, which the StageTracker routes above the checklist", forbidden)
		}
	}
	if !strings.Contains(body, "logger.") {
		t.Error("watchAppsBudget no longer reports through the logger at all")
	}
}

// functionBody returns the source of the named top-level function with
// comments stripped, so a guard cannot be satisfied by its own explanation.
func functionBody(t *testing.T, src, name string) string {
	t.Helper()
	start := strings.Index(src, "\nfunc "+name+"(")
	if start < 0 {
		t.Fatalf("no function %s in local.go", name)
	}
	rest := src[start+1:]
	end := strings.Index(rest, "\n}\n")
	if end < 0 {
		t.Fatalf("function %s never closes", name)
	}
	body := rest[:end]
	return regexp.MustCompile(`(?m)//.*$`).ReplaceAllString(body, "")
}
