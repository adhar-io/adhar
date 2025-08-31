/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the file at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package helpers

import (
	"github.com/charmbracelet/lipgloss"
)

// Common styles for CLI commands
var (
	// Define common base colors
	PrimaryColor   = lipgloss.AdaptiveColor{Light: "#0366d6", Dark: "#58a6ff"}
	SecondaryColor = lipgloss.AdaptiveColor{Light: "#28a745", Dark: "#3fb950"}
	AccentColor    = lipgloss.AdaptiveColor{Light: "#6f42c1", Dark: "#8957e5"}
	ErrorColor     = lipgloss.AdaptiveColor{Light: "#cb2431", Dark: "#f85149"}
	WarningColor   = lipgloss.AdaptiveColor{Light: "#f66a0a", Dark: "#f0883e"}
	InfoColor      = lipgloss.AdaptiveColor{Light: "#0090ff", Dark: "#00b4ff"}
	HighlightColor = lipgloss.AdaptiveColor{Light: "#e36209", Dark: "#ffab70"}
	SuccessColor   = lipgloss.AdaptiveColor{Light: "#10b981", Dark: "#3fb950"}

	// Define common styles
	HeaderStyle = lipgloss.NewStyle().
			Foreground(PrimaryColor).
			Bold(true).
			MarginBottom(1)

	TitleStyle = lipgloss.NewStyle().
			Foreground(AccentColor).
			Bold(true).
			MarginLeft(1)

	SubtitleStyle = lipgloss.NewStyle().
			Foreground(SecondaryColor).
			Italic(true).
			MarginLeft(2)

	SuccessStyle = lipgloss.NewStyle().
			Foreground(SecondaryColor).
			Bold(true)

	ErrorStyle = lipgloss.NewStyle().
			Foreground(ErrorColor).
			Bold(true)

	WarningStyle = lipgloss.NewStyle().
			Foreground(WarningColor).
			Bold(true)

	InfoStyle = lipgloss.NewStyle().
			Foreground(InfoColor).
			Italic(true)

	HighlightStyle = lipgloss.NewStyle().
			Foreground(HighlightColor).
			Bold(true)

	BulletStyle = lipgloss.NewStyle().
			Foreground(InfoColor)

	CmdDescStyle = lipgloss.NewStyle().
			Foreground(SecondaryColor).
			Italic(true)

	BorderStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(PrimaryColor).
			Padding(1).
			MarginTop(1).
			MarginBottom(1)

	FocusedStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(HighlightColor).
			Padding(1).
			MarginTop(1).
			MarginBottom(1)

	TableHeaderStyle = lipgloss.NewStyle().
				Foreground(PrimaryColor).
				Bold(true).
				Padding(0, 1)

	TableCellStyle = lipgloss.NewStyle().
			Padding(0, 1)

	TableRowStyle = lipgloss.NewStyle().
			Padding(0, 0, 0, 2)

	CodeStyle = lipgloss.NewStyle().
			Foreground(PrimaryColor).
			Background(AccentColor).
			Padding(0, 1).
			Border(lipgloss.NormalBorder()).
			BorderForeground(InfoColor)

	LinkStyle = lipgloss.NewStyle().
			Foreground(SecondaryColor).
			Underline(true)

	MutedStyle = lipgloss.NewStyle().
			Foreground(AccentColor).
			Italic(true)

	BoldStyle = lipgloss.NewStyle().
			Bold(true)

	UrlStyle = lipgloss.NewStyle().
			Foreground(InfoColor).
			Underline(true)
)

// CreateBox creates a bordered box with the given content
func CreateBox(content string, width int) string {
	return BorderStyle.Width(width).Render(content)
}

// CreateSection creates a styled section header
func CreateSection(title string) string {
	return TitleStyle.Render(title)
}

// CreateHighlight creates highlighted text
func CreateHighlight(text string) string {
	return HighlightStyle.Render(text)
}

// CreateInfo creates info text
func CreateInfo(text string) string {
	return InfoStyle.Render(text)
}

// CreateSuccess creates success text
func CreateSuccess(text string) string {
	return SuccessStyle.Render(text)
}

// CreateError creates error text
func CreateError(text string) string {
	return ErrorStyle.Render(text)
}

// CreateWarning creates warning text
func CreateWarning(text string) string {
	return WarningStyle.Render(text)
}

// CreateCode creates code-style text
func CreateCode(text string) string {
	return CodeStyle.Render(text)
}

// CreateLink creates link-style text
func CreateLink(text string) string {
	return LinkStyle.Render(text)
}

// CreateMuted creates muted text
func CreateMuted(text string) string {
	return MutedStyle.Render(text)
}

// CreateAccent creates an accent text with highlight styling
func CreateAccent(text string) string {
	return HighlightStyle.Render(text)
}
