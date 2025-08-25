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
	// Title style for section headers
	TitleStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#8b5cf6")).
			Bold(true).
			MarginRight(1)

	// Header style for main headers
	HeaderStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#8b5cf6")).
			Bold(true).
			MarginRight(1)

	// Highlight style for important information
	HighlightStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#06b6d4")).
			Bold(true)

	// Subtitle style for secondary headers
	SubtitleStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#64748b")).
			Bold(true).
			MarginTop(1).
			MarginBottom(1)

	// Command description style
	CmdDescStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#64748b")).
			Italic(true)

	// Bullet style for bullet points
	BulletStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#8b5cf6")).
			Bold(true)

	// Info style for additional information
	InfoStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#64748b")).
			Italic(true)

	// Success style for positive status
	SuccessStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#10b981")).
			Bold(true)

	// Error style for error messages
	ErrorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#ef4444")).
			Bold(true)

	// Warning style for warnings
	WarningStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#f59e0b")).
			Bold(true)

	// Border style for boxes and containers
	BorderStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#8b5cf6")).
			Padding(1, 2).
			Margin(1, 0)

	// Code style for technical information
	CodeStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#f1f5f9")).
			Background(lipgloss.Color("#1e293b")).
			Padding(0, 1).
			Border(lipgloss.NormalBorder()).
			BorderForeground(lipgloss.Color("#475569"))

	// Link style for URLs and references
	LinkStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#3b82f6")).
			Underline(true)

	// Muted style for less important text
	MutedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#94a3b8")).
			Italic(true)

	// Cluster status styles
	ClusterStatusStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#10b981")).
				Bold(true)

	ClusterErrorStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#ef4444")).
				Bold(true)

	ClusterWarningStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#f59e0b")).
				Bold(true)

	// Provider styles
	ProviderStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#8b5cf6")).
			Bold(true)

	// Region style
	RegionStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#06b6d4")).
			Italic(true)

	// Version style
	VersionStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#f59e0b")).
			Bold(true)

	// Accent style for highlighting
	AccentStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#ec4899")).
			Bold(true)

	// Bold style for emphasized text
	BoldStyle = lipgloss.NewStyle().
			Bold(true)

	// URL style for links
	UrlStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#3b82f6")).
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

// CreateAccent creates accent text
func CreateAccent(text string) string {
	return AccentStyle.Render(text)
}
