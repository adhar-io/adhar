package helpers

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// The "Setup completed in:" line under `adhar up`'s Next Steps started at
// column 0 while the heading and steps around it sat at the gutter — the same
// slip as on `adhar down` (2026-10-21).
func TestSetupCompletedLineStartsAtTheGutter(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = w
	NewProgressTracker("Provisioning", []string{"one"}).CompleteStyled()
	os.Stdout = stdout
	_ = w.Close()
	b, _ := io.ReadAll(r)

	gutter := strings.Repeat(" ", BannerGutter)
	found := false
	for _, line := range strings.Split(ansi.Strip(string(b)), "\n") {
		if !strings.Contains(line, "Setup completed in:") && !strings.Contains(line, "Next Steps:") {
			continue
		}
		found = true
		if !strings.HasPrefix(line, gutter) {
			t.Errorf("starts at column 0, not %d: %q", BannerGutter, line)
		}
	}
	if !found {
		t.Fatal("the completion summary no longer prints these lines; this test checks nothing")
	}
}
