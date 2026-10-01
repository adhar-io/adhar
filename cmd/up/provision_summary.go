package up

import (
	"fmt"
	"strings"

	"adhar-io/adhar/cmd/helpers"
	"github.com/charmbracelet/lipgloss"
)

// renderProvisionSummary closes out a multi-environment `adhar up`.
//
// It replaced a block that announced "● Platform Provisioning Complete!" whatever
// happened — including immediately above "Error: failed to provision 1 out of 1
// environments" — inside a box drawn from literal dashes with the count padded by
// a fixed run of spaces, so the right-hand border drifted as soon as a number was
// not one digit:
//
//	┌─────────────────────────────────────────────┐
//	│ Environments Provisioned: 0/1              │
//	└─────────────────────────────────────────────┘
//
// The headline now follows the outcome, the frame is measured rather than typed,
// and the failures are named: on a long run the per-environment lines have
// scrolled away by the time the summary prints, so "which one broke, and why" has
// to be repeated here.
const (
	// One grid for every row; sized to match the teardown confirmation panel so the
	// two read as the same CLI.
	summaryLabelWidth = 14
	summaryValueWidth = 54
	summaryPanelWidth = 74
)

func renderProvisionSummary(succeeded, total int, failures []string) string {
	var (
		icon  string
		head  string
		color lipgloss.TerminalColor
	)
	switch {
	case total == 0:
		return ""
	case succeeded == total:
		icon, head, color = helpers.IconReady, "Platform provisioning complete", helpers.SecondaryColor
	case succeeded == 0:
		icon, head, color = helpers.IconFailed, "Platform provisioning failed", helpers.ErrorColor
	default:
		// Some up, some not. Neither "complete" nor "failed" describes it, and
		// calling it either one hides half the result.
		icon, head, color = helpers.IconDegraded, "Platform provisioning incomplete", helpers.WarningColor
	}

	// One label column for every row. The two rows were formatted independently
	// (a hardcoded run of spaces on one, %-15s on the other), so their values
	// started two columns apart.
	row := func(label, value string) string {
		return fmt.Sprintf("  %-*s%s\n", summaryLabelWidth, label,
			helpers.WrapValue(value, summaryValueWidth, summaryLabelWidth+2))
	}

	var b strings.Builder
	b.WriteString(lipgloss.NewStyle().Foreground(color).Bold(true).Render(icon+"  "+head) + "\n\n")
	b.WriteString(row("Environments", fmt.Sprintf("%d of %d provisioned", succeeded, total)))
	for i, f := range failures {
		label := "Failed"
		if i > 0 {
			label = "" // continuation: the column stays, the word does not repeat
		}
		b.WriteString(row(label, f))
	}

	// Fixed width, so the frame does not change shape with the outcome.
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(color).
		Padding(1, 2).
		MarginTop(1).
		Width(summaryPanelWidth).
		Render(strings.TrimRight(b.String(), "\n"))
}
