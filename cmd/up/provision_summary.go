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
		b.WriteString(row(label, condenseFailure(f)))
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

// condenseFailure makes a cloud SDK error fit a panel row.
//
// WrapValue wraps on width, but it cannot save a value that already contains
// NEWLINES: each one restarts the line at the frame's own indent instead of the
// value column, and any token longer than the column (a token URL, or an 80-dash
// divider) cannot be broken at all. An Azure auth failure on 2026-10-04 carried
// all three — a POST line, a 401 banner, two dash dividers, the full JSON body,
// trace and correlation ids — and the panel printed this:
//
//	│  9889-34c2ffd38528/oauth2/v2.0/token                                     │
//	│                  ------------------------------------------------------  │
//	│  --------------------------                                              │
//
// The full error is printed in the `Error:` line anyway, so the panel's job is
// to say which environment failed and what to do about it — not to reproduce a
// stack of HTTP framing. The remedy ExplainAccessError appends is kept whole and
// the cause is what gets shortened, because the remedy is the actionable half.
func condenseFailure(s string) string {
	cause, remedy := s, ""
	if i := strings.Index(s, remedyMarker); i >= 0 {
		cause, remedy = s[:i], strings.TrimSpace(s[i+len(remedyMarker):])
	}
	cause = flattenForPanel(cause)
	if r := []rune(cause); len(r) > maxCauseRunes {
		cut := maxCauseRunes
		// Prefer a word boundary, but only a nearby one — backing up through a
		// long token would throw away most of the line.
		if i := strings.LastIndex(string(r[:cut]), " "); i > maxCauseRunes-20 {
			cut = len([]rune(string(r[:cut])[:i]))
		}
		cause = strings.TrimRight(string(r[:cut]), " ,:;-") + "…"
	}
	if remedy == "" {
		return cause
	}
	return cause + " → " + flattenForPanel(remedy)
}

// remedyMarker is how ExplainAccessError's advice is attached to an error.
const remedyMarker = "\n\n  → "

// maxCauseRunes caps the cause so the remedy is still visible without scrolling
// past a wall of HTTP framing.
const maxCauseRunes = 150

// flattenForPanel reduces text to a single line of breakable tokens.
func flattenForPanel(s string) string {
	var out []string
	for _, tok := range strings.Fields(s) {
		// Dash and equals rules are pure framing; they carry no information and
		// are the widest unbreakable tokens in an SDK dump.
		if t := strings.Trim(tok, "-="); t == "" && tok != "" {
			continue
		}
		for len([]rune(tok)) > summaryValueWidth {
			r := []rune(tok)
			out = append(out, string(r[:summaryValueWidth]))
			tok = string(r[summaryValueWidth:])
		}
		out = append(out, tok)
	}
	return strings.Join(out, " ")
}
