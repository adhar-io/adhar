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

import "github.com/charmbracelet/lipgloss"

// The CLI's icon vocabulary.
//
// These are single-cell geometric glyphs from the Unicode symbol blocks, not
// emoji, and they carry meaning through COLOUR rather than through a picture.
// Two reasons, one aesthetic and one practical:
//
//   - Consistency: a defined set reads as one product. A different emoji per
//     call site reads as decoration.
//   - Alignment: emoji are double-width, and many (✅, ⚙️, 🏷️) carry a
//     variation selector that terminals measure inconsistently — which is what
//     pushed table columns out of true. Every glyph here is exactly one cell in
//     every terminal, so a table built with Table stays aligned.
//
// Use the State* helpers for state so colour and glyph never disagree.
const (
	IconReady    = "●" // healthy, running, ready
	IconPending  = "◌" // starting, progressing, not yet ready
	IconDegraded = "▲" // degraded but serving
	IconFailed   = "✖" // failed, unavailable
	IconUnknown  = "◍" // state could not be determined
	IconDisabled = "○" // present but switched off

	IconControlPlane = "◆" // control-plane node
	IconWorker       = "●" // worker node
	IconNodeOther    = "◇" // any other node role

	IconCluster   = "⎔" // cluster / platform
	IconNamespace = "▤" // namespace
	IconApp       = "▣" // application / workload
	IconStorage   = "▥" // volume, storage class
	IconNetwork   = "⇄" // route, service, gateway
	IconSecurity  = "⛨" // policy, certificate, secret
	IconBullet    = "▸" // list item
	IconArrow     = "→" // "leads to", indentation of detail
)

// Status glyph + colour pairs, so every command reports state identically.
var (
	statusReadyStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#10b981"))
	statusPendingStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#f59e0b"))
	statusWarningStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#f59e0b"))
	statusErrorStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#ef4444"))
	statusUnknownStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#64748b"))
	statusDisabledStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#64748b"))
)

// StateReady renders a healthy state, e.g. StateReady("Ready").
func StateReady(label string) string { return statusReadyStyle.Render(IconReady + " " + label) }

// StatePending renders an in-progress state.
func StatePending(label string) string { return statusPendingStyle.Render(IconPending + " " + label) }

// StateDegraded renders a degraded-but-serving state.
func StateDegraded(label string) string { return statusWarningStyle.Render(IconDegraded + " " + label) }

// StateFailed renders a failed state.
func StateFailed(label string) string { return statusErrorStyle.Render(IconFailed + " " + label) }

// StateUnknown renders an indeterminate state.
func StateUnknown(label string) string { return statusUnknownStyle.Render(IconUnknown + " " + label) }

// StateDisabled renders a switched-off state.
func StateDisabled(label string) string {
	return statusDisabledStyle.Render(IconDisabled + " " + label)
}

// SectionHeading renders a titled section, e.g. SectionHeading(IconCluster, "Cluster Nodes").
func SectionHeading(icon, title string) string {
	return TitleStyle.Render(icon + "  " + title)
}
