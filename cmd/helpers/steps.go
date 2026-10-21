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

// RenderReadyPanel is the end-of-run success block: a ✓ header, a bordered
// access table (service → URL), then a "Next Steps:" list. access is
// [label, URL] pairs; steps is [command, purpose] pairs, rendered
// "→ Run <command> to <purpose>".
func RenderReadyPanel(access, steps [][2]string) string {
	linkStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(brandBlue.hex())).Underline(true)
	nameStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(brandPurple.hex())).Bold(true)
	okStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#10B981")).Bold(true)

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
		// The lines around this box are written with a literal two-space
		// prefix; the box needs the same margin or it hangs to their left.
		MarginLeft(BannerGutter).
		Render(strings.Join(rows, "\n"))

	var b strings.Builder
	b.WriteString("  " + okStyle.Render("✓") + "  " + stepTitleStyle.Render("Platform ready") + "\n\n")
	b.WriteString(box + "\n")
	// The commands to run next, in the same "Next Steps:" form `adhar down` and
	// the setup summary use, so every command ends the same way.
	if len(steps) > 0 {
		b.WriteString("\n" + TitleStyle.Render("Next Steps:"))
		for _, st := range steps {
			b.WriteString(fmt.Sprintf("\n  → Run %s to %s", HighlightStyle.Render(st[0]), st[1]))
		}
	}
	return b.String()
}
