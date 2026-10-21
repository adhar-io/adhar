package up

import (
	"os"
	"strings"
	"testing"
)

// Both checklists — Kind and cloud — must give the structured logger the
// QuietLog, not the bare TrackerWriter, or INFO lines scroll above the steps
// again (2026-10-21).
func TestBothChecklistsHoldInfoLogs(t *testing.T) {
	for _, f := range []string{"local.go", "cloud_progress.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		if !strings.Contains(src, "logger.SetOutput(quiet)") {
			t.Errorf("%s routes the logger without holding INFO", f)
		}
		if strings.Contains(src, "logger.SetOutput(tw)") || strings.Contains(src, "stdlog.SetOutput(tw)") {
			t.Errorf("%s still sends a log stream straight to the checklist", f)
		}
		if !strings.Contains(src, "helpers.RenderHeld(quiet.Held())") {
			t.Errorf("%s never prints the held lines when the run fails", f)
		}
	}
}
