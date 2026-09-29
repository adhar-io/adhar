package helpers

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// The heart is red, and it is coloured INDEPENDENTLY of the surrounding text —
// the rest of the sign-off keeps the muted subtitle styling.
//
// It is also defined in one place now. The line used to be spelled out at two
// call sites, which is how they drift: a bulk edit of the CLI's icon set replaced
// the heart with the success glyph in one of them, and the footer read
// "Built with ✓ for developers!" until it was noticed.
func TestSignOffPaintsTheHeartRed(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prev)

	got := SignOff(SubtitleStyle)

	if !strings.Contains(got, "❤") {
		t.Fatalf("the heart is missing: %q", got)
	}
	// The red must be emitted as its own sequence around the heart, not inherited.
	red := lipgloss.NewStyle().Foreground(HeartColor).Render(heartGlyph)
	if !strings.Contains(got, red) {
		t.Errorf("the heart is not painted with HeartColor.\n got: %q\nwant substring: %q", got, red)
	}
	for _, word := range []string{"Adhar", "Built with", "for developers!"} {
		if !strings.Contains(got, word) {
			t.Errorf("sign-off lost %q: %q", word, got)
		}
	}
}

// HeartColor is deliberately separate from ErrorColor. They are both red today,
// but a heart is not an error and must not follow error styling if that changes.
func TestHeartColourIsNotTheErrorColour(t *testing.T) {
	if HeartColor == ErrorColor {
		t.Error("HeartColor should be its own colour, so error restyling cannot change the brand mark")
	}
}

// --no-color and any path that captures output rather than rendering it needs the
// unstyled form.
func TestSignOffTextCarriesNoStyling(t *testing.T) {
	if strings.Contains(SignOffText, "\x1b") {
		t.Errorf("SignOffText must be plain: %q", SignOffText)
	}
	if !strings.Contains(SignOffText, "❤") {
		t.Errorf("SignOffText lost the heart: %q", SignOffText)
	}
}

// The sign-off is ONE space either side of the heart.
//
// The heart has to be painted separately from the surrounding text (a nested
// colour resets to the default, not to the outer colour), which means `text` is
// rendered in two fragments — and a lipgloss style applies its margins to every
// fragment it renders. With SubtitleStyle's MarginLeft(2) that turned the single
// space before "for" into three: "Built with ❤   for developers!".
func TestSignOffSpacesTheHeartOnce(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.Ascii) // no escapes, so spacing is what is left
	defer lipgloss.SetColorProfile(prev)

	for _, style := range []struct {
		name string
		s    lipgloss.Style
	}{
		{"SubtitleStyle (MarginLeft 2)", SubtitleStyle},
		{"no margin", lipgloss.NewStyle()},
		{"MarginLeft 6", lipgloss.NewStyle().MarginLeft(6)},
	} {
		got := SignOff(style.s)
		if want := "Built with " + heartGlyph + " for developers!"; !strings.Contains(got, want) {
			t.Errorf("%s: spacing around the heart is wrong.\n got: %q\nwant substring: %q", style.name, got, want)
		}
		// The caller's margin is applied once, to the whole line — not per fragment.
		if want := spaces(style.s.GetMarginLeft()) + "Adhar"; !strings.HasPrefix(got, want) {
			t.Errorf("%s: margin is not applied exactly once.\n got: %q\nwant prefix: %q", style.name, got, want)
		}
	}
}

// The heart is the emoji presentation, which is the larger glyph. Bare U+2764
// renders small and monochrome on most terminals.
func TestHeartUsesEmojiPresentation(t *testing.T) {
	if !strings.Contains(heartGlyph, "\ufe0f") {
		t.Errorf("heartGlyph should carry the U+FE0F variation selector for the emoji form: %q", heartGlyph)
	}
}
