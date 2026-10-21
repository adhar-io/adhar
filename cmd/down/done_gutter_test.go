package down

import (
	"strings"
	"testing"
	"time"

	"adhar-io/adhar/cmd/helpers"
	"github.com/charmbracelet/x/ansi"
)

// The finished teardown screen is plain lines under a bordered panel, and the
// "Teardown completed in:" line was the one that started at column 0 while the
// heading and the steps above it sat at the gutter (2026-10-21).
func TestTheFinishedTeardownScreenStartsAtTheGutter(t *testing.T) {
	m := downModel{done: true, startTime: time.Now().Add(-5 * time.Second)}
	assertAtGutter(t, m.View())
}

func assertAtGutter(t *testing.T, out string) {
	t.Helper()
	gutter := strings.Repeat(" ", helpers.BannerGutter)
	sawTiming := false
	for i, line := range strings.Split(ansi.Strip(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.Contains(line, "completed in:") {
			sawTiming = true
		}
		if !strings.HasPrefix(line, gutter) {
			t.Errorf("line %d starts at column 0, not %d: %q", i, helpers.BannerGutter, line)
		}
	}
	if !sawTiming {
		t.Fatal("the screen no longer shows how long the run took; this test checks nothing")
	}
}
