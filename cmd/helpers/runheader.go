package helpers

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// runHeaderWidth matches the teardown confirmation and the provisioning summary,
// so every panel in one session is the same shape.
const runHeaderWidth = 74

// RenderRunHeader frames the "what am I about to run" summary a command prints
// before it starts: label/value rows in a border.
//
// It was three loose `fmt.Printf` lines with hand-counted indentation, which left
// the values hanging in open space directly above whatever logged next — so the
// run's own parameters were indistinguishable from its output. Takes ordered pairs
// rather than a map because the order is the point: mode, then config, then scope.
func RenderRunHeader(rows [][2]string) string {
	if len(rows) == 0 {
		return ""
	}
	label := 0
	for _, r := range rows {
		if n := len(r[0]); n > label {
			label = n
		}
	}
	label += 3 // gutter between the label and its value

	var b strings.Builder
	for i, r := range rows {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(fmt.Sprintf("%s%s",
			MutedStyle.Render(fmt.Sprintf("%-*s", label, r[0])),
			InfoStyle.Render(WrapValue(r[1], runHeaderWidth-label-6, label))))
	}

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		// Grey, not a brand colour: this panel states facts the operator already
		// typed, so it should recede next to the warning and result panels.
		BorderForeground(taglineGray).
		Padding(0, 2).
		MarginTop(1).
		Width(runHeaderWidth).
		Render(b.String())
}
