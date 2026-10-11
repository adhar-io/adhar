package helpers

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// contentIndents returns, for each non-blank row strictly inside a bordered
// block, how many cells sit between the left border and the first glyph.
func contentIndents(t *testing.T, rendered string) []int {
	t.Helper()
	var indents []int
	for _, line := range strings.Split(rendered, "\n") {
		plain := ansi.Strip(line)
		i := strings.Index(plain, "│")
		if i < 0 {
			continue // top/bottom frame or outer whitespace
		}
		inner := plain[i+len("│"):]
		if j := strings.LastIndex(inner, "│"); j >= 0 {
			inner = inner[:j]
		}
		if strings.TrimSpace(inner) == "" {
			continue // padding row
		}
		indents = append(indents, ansi.StringWidth(inner)-ansi.StringWidth(strings.TrimLeft(inner, " ")))
	}
	return indents
}

// Inside a bordered panel every content row must start at the same column.
//
// BorderStyle already carries MarginLeft(BannerGutter) and Padding(1), so the
// box is offset from the terminal edge and its contents sit one cell inside the
// frame. Nesting TitleStyle or SubtitleStyle in there adds their own
// MarginLeft(2) on top: measured on `adhar down`, the heading started three
// cells in while the spinner and elapsed-time rows started at one, and because
// the box is sized to its widest line the heading then ran up against the right
// border with a single space while the other rows had slack.
//
// PanelTitleStyle and PanelSubtitleStyle exist for this, and this test asserts
// the PROPERTY rather than their definitions — a style is only safe to nest if
// the rendered rows actually line up.
func TestBorderedPanelContentSharesOneIndent(t *testing.T) {
	const width = 60
	body := PanelTitleStyle.Render("Deleting cloud resources — this can take several minutes") +
		fmt.Sprintf("\n\n%s %s", "⠼", PanelTitleStyle.Render("Tearing down dev")) +
		PanelSubtitleStyle.Render(" · deleting 'adhar' on gcp") +
		fmt.Sprintf("\n%s %s", InfoStyle.Render("Elapsed time:"), "19s")

	indents := contentIndents(t, BorderStyle.Width(width).Render(body))
	if len(indents) < 3 {
		t.Fatalf("expected at least 3 content rows, measured %d — this guard is checking nothing", len(indents))
	}
	for i, got := range indents {
		if got != indents[0] {
			t.Errorf("content row %d is indented %d cells, row 0 is %d: a bordered panel's rows must "+
				"share one left edge, and a style with its own MarginLeft nested inside the border is "+
				"what breaks it", i, got, indents[0])
		}
	}

	// And prove the failure mode is real, so the test above cannot pass for the
	// wrong reason: the gutter-carrying variants DO misalign in the same frame.
	bad := TitleStyle.Render("Heading") + fmt.Sprintf("\n%s %s", InfoStyle.Render("Elapsed time:"), "19s")
	badIndents := contentIndents(t, BorderStyle.Width(width).Render(bad))
	if len(badIndents) == 2 && badIndents[0] == badIndents[1] {
		t.Error("TitleStyle no longer adds a left margin, so PanelTitleStyle is redundant; collapse the " +
			"two rather than keeping a distinction that no longer exists")
	}
}

// The panel variants must carry no left margin, which is the whole point of
// them. Checked directly as well as through the rendering above, so a change to
// the base styles cannot quietly reintroduce one.
func TestPanelStylesCarryNoGutter(t *testing.T) {
	for name, style := range map[string]struct{ have, want int }{
		"PanelTitleStyle":    {PanelTitleStyle.GetMarginLeft(), 0},
		"PanelSubtitleStyle": {PanelSubtitleStyle.GetMarginLeft(), 0},
	} {
		if style.have != style.want {
			t.Errorf("%s has MarginLeft(%d), want %d: it is rendered inside a border that already pays "+
				"the gutter", name, style.have, style.want)
		}
	}
	// The non-panel ones must keep theirs, or the standalone headings lose the
	// CLI's single left margin.
	if TitleStyle.GetMarginLeft() != BannerGutter {
		t.Errorf("TitleStyle has MarginLeft(%d), want BannerGutter(%d): headings printed outside a panel "+
			"share the banner's gutter", TitleStyle.GetMarginLeft(), BannerGutter)
	}
}
