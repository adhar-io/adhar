/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package helpers

// steps.go renders the end-of-run "Platform ready" panel for `adhar up`. The
// live provisioning progress itself is rendered by the StageTracker
// (stagetracker.go); this file just draws the final access table and hints.

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	stepTitleStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color(brandBlue.hex())).Bold(true)
	stepDetailStyle = lipgloss.NewStyle().Foreground(taglineGray)
)

// RenderReadyPanel is the end-of-run success block: a ● header, a bordered
// access table (service → URL), then labelled command hints. access and hints
// are [label, value] pairs.
//
// Use it ONLY when the platform's entry point is actually serving. When the
// cluster is up but the apps have not converged, use RenderConvergingPanel:
// the two differ by one glyph and one line of text, and that difference is the
// whole point — this one is a promise the reader will act on.
func RenderReadyPanel(access, hints [][2]string) string {
	return renderPanel("✓", "#10B981", "Platform ready", "", access, hints)
}

// RenderConvergingPanel is the same block for a platform that is UP but not yet
// serving: an amber ◌ instead of a green ✓, and a headline saying so above the
// URL table.
//
// The URLs are still listed, deliberately. They are correct — they are where the
// services will be, the operator will want them shortly, and omitting them sends
// someone to the docs to reconstruct hostnames the tool already knows. What
// changes is that the panel no longer claims they work.
func RenderConvergingPanel(headline string, access, hints [][2]string) string {
	return renderPanel("◌", "#F59E0B", "Platform converging", headline, access, hints)
}

// renderPanel is the shared body. Extracted when the converging variant was
// added, so the two endings cannot drift apart in layout the way the cloud and
// local success messages once did.
func renderPanel(glyph, glyphColor, title, headline string, access, hints [][2]string) string {
	linkStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(brandBlue.hex())).Underline(true)
	nameStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(brandPurple.hex())).Bold(true)
	okStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(glyphColor)).Bold(true)
	cmdStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(brandBlue.hex())).Bold(true)

	nameW := 0
	for _, r := range access {
		if w := lipgloss.Width(r[0]); w > nameW {
			nameW = w
		}
	}
	rows := make([]string, 0, len(access))
	for _, r := range access {
		pad := strings.Repeat(" ", nameW-lipgloss.Width(r[0]))
		rows = append(rows, fmt.Sprintf("%s%s   %s", nameStyle.Render(r[0]), pad, linkStyle.Render(r[1])))
	}
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(mix(brandBlue, brandPurple, 0.5).hex())).
		Padding(0, 2).
		Render(strings.Join(rows, "\n"))

	var b strings.Builder
	b.WriteString("  " + okStyle.Render(glyph) + "  " + stepTitleStyle.Render(title) + "\n\n")
	if headline != "" {
		// Wrapped to the panel width so a long reason does not run off the
		// terminal; WrapValue is what the teardown and provisioning summaries use.
		b.WriteString("  " + stepDetailStyle.Render(WrapValue(headline, 70, 2)) + "\n\n")
	}
	b.WriteString(box + "\n")
	hintW := 0
	for _, h := range hints {
		if w := lipgloss.Width(h[0]); w > hintW {
			hintW = w
		}
	}
	for _, h := range hints {
		pad := strings.Repeat(" ", hintW-lipgloss.Width(h[0]))
		b.WriteString(fmt.Sprintf("\n  %s%s   %s", stepDetailStyle.Render(h[0]), pad, cmdStyle.Render(h[1])))
	}
	return b.String()
}
