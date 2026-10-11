package helpers

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// The banner shares the help body's left edge. Every line of `adhar help` is
// printed behind a two-space gutter; the art and tagline used to start at
// column 0 and hung to the left of the prose beneath them.
func TestBannerSharesTheHelpGutter(t *testing.T) {
	if BannerGutter != 2 {
		t.Fatalf("BannerGutter = %d; the help body in cmd/helptext.go indents by exactly two spaces", BannerGutter)
	}
	gutter := strings.Repeat(" ", BannerGutter)
	lines := strings.Split(ansi.Strip(RenderBanner()), "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !strings.HasPrefix(line, gutter) {
			t.Errorf("banner line %d does not start behind the %d-column gutter: %q", i, BannerGutter, line)
		}
	}
	// The tagline is the narrower block and marks the left edge: it must sit AT
	// the gutter (plus at most one centering space), not further in.
	tag := lines[len(lines)-1]
	if !strings.HasPrefix(tag, gutter+"O P E N") && !strings.HasPrefix(tag, gutter+" O P E N") {
		t.Errorf("tagline should start at the gutter: %q", tag)
	}
}
