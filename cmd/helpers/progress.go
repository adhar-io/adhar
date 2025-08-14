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

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/lipgloss"
)

// ============================================================================
// ENHANCED PROGRESS SYSTEM
// Consolidated progress tracking for all long-running commands
// ============================================================================

// Define styles for progress system
var (
	// Define some base colors
	primaryColor   = lipgloss.AdaptiveColor{Light: "#0366d6", Dark: "#58a6ff"}
	secondaryColor = lipgloss.AdaptiveColor{Light: "#28a745", Dark: "#3fb950"}
	accentColor    = lipgloss.AdaptiveColor{Light: "#6f42c1", Dark: "#8957e5"}
	errorColor     = lipgloss.AdaptiveColor{Light: "#cb2431", Dark: "#f85149"}
	warningColor   = lipgloss.AdaptiveColor{Light: "#f66a0a", Dark: "#f0883e"}
	infoColor      = lipgloss.AdaptiveColor{Light: "#0090ff", Dark: "#00b4ff"}
	highlightColor = lipgloss.AdaptiveColor{Light: "#e36209", Dark: "#ffab70"}

	// Define styles for progress system
	headerStyle = lipgloss.NewStyle().
			Foreground(primaryColor).
			Bold(true)

	titleStyle = lipgloss.NewStyle().
			Foreground(accentColor).
			Bold(true)

	subtitleStyle = lipgloss.NewStyle().
			Foreground(secondaryColor).
			Italic(true)

	successStyle = lipgloss.NewStyle().
			Foreground(secondaryColor).
			Bold(true)

	errorStyle = lipgloss.NewStyle().
			Foreground(errorColor).
			Bold(true)

	warningStyle = lipgloss.NewStyle().
			Foreground(warningColor).
			Bold(true)

	infoStyle = lipgloss.NewStyle().
			Foreground(infoColor).
			Italic(true)

	highlightStyle = lipgloss.NewStyle().
			Foreground(highlightColor).
			Bold(true)
)

// Progress tracking structures
type ProgressStep struct {
	Name        string
	Description string
	Status      ProgressStatus
	StartTime   time.Time
	EndTime     time.Time
	Error       error
}

type ProgressStatus int

const (
	StatusPending ProgressStatus = iota
	StatusInProgress
	StatusCompleted
	StatusFailed
	StatusSkipped
)

func (s ProgressStatus) String() string {
	switch s {
	case StatusPending:
		return "⏳"
	case StatusInProgress:
		return "🔄"
	case StatusCompleted:
		return "✅"
	case StatusFailed:
		return "❌"
	case StatusSkipped:
		return "⏭️"
	default:
		return "❓"
	}
}

// ProgressTracker manages multiple steps with visual feedback
type ProgressTracker struct {
	Title       string
	Steps       []ProgressStep
	CurrentStep int
	StartTime   time.Time
	Width       int
	ShowSpinner bool
	Spinner     spinner.Model
	Quiet       bool // For non-interactive usage
}

// NewProgressTracker creates a new progress tracker
func NewProgressTracker(title string, stepNames []string) *ProgressTracker {
	steps := make([]ProgressStep, len(stepNames))
	for i, name := range stepNames {
		steps[i] = ProgressStep{
			Name:   name,
			Status: StatusPending,
		}
	}

	// Initialize spinner
	s := spinner.New()
	s.Spinner = spinner.Spinner{
		Frames: []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"},
		FPS:    10,
	}
	s.Style = successStyle

	return &ProgressTracker{
		Title:       title,
		Steps:       steps,
		CurrentStep: 0,
		StartTime:   time.Now(),
		Width:       50,
		ShowSpinner: true,
		Spinner:     s,
		Quiet:       false,
	}
}

// StartStep marks a step as in progress
func (p *ProgressTracker) StartStep(stepIndex int, description string) {
	if stepIndex >= 0 && stepIndex < len(p.Steps) {
		p.Steps[stepIndex].Status = StatusInProgress
		p.Steps[stepIndex].Description = description
		p.Steps[stepIndex].StartTime = time.Now()
		p.CurrentStep = stepIndex
		if !p.Quiet {
			p.Render()
		}
	}
}

// CompleteStep marks a step as completed
func (p *ProgressTracker) CompleteStep(stepIndex int) {
	if stepIndex >= 0 && stepIndex < len(p.Steps) {
		p.Steps[stepIndex].Status = StatusCompleted
		p.Steps[stepIndex].EndTime = time.Now()
		if !p.Quiet {
			p.Render()
		}
	}
}

// FailStep marks a step as failed
func (p *ProgressTracker) FailStep(stepIndex int, err error) {
	if stepIndex >= 0 && stepIndex < len(p.Steps) {
		p.Steps[stepIndex].Status = StatusFailed
		p.Steps[stepIndex].Error = err
		p.Steps[stepIndex].EndTime = time.Now()
		if !p.Quiet {
			p.Render()
		}
	}
}

// SkipStep marks a step as skipped
func (p *ProgressTracker) SkipStep(stepIndex int, reason string) {
	if stepIndex >= 0 && stepIndex < len(p.Steps) {
		p.Steps[stepIndex].Status = StatusSkipped
		p.Steps[stepIndex].Description = reason
		p.Steps[stepIndex].EndTime = time.Now()
		if !p.Quiet {
			p.Render()
		}
	}
}

// GetOverallProgress returns the overall progress percentage
func (p *ProgressTracker) GetOverallProgress() float64 {
	completed := 0
	for _, step := range p.Steps {
		if step.Status == StatusCompleted || step.Status == StatusSkipped {
			completed++
		}
	}
	return float64(completed) / float64(len(p.Steps))
}

// renderProgressBar renders an enhanced progress bar with better styling
func renderProgressBar(percent float64, width int, showPercent bool) string {
	w := width - 2 // Account for brackets
	if showPercent {
		w -= 5 // Account for percentage text
	}

	fill := int(percent * float64(w))
	empty := w - fill

	var bar string
	if showPercent {
		bar = fmt.Sprintf("[%s%s] %3.0f%%",
			strings.Repeat("█", fill),
			strings.Repeat("░", empty),
			percent*100)
	} else {
		bar = fmt.Sprintf("[%s%s]",
			strings.Repeat("█", fill),
			strings.Repeat("░", empty))
	}

	// Color the bar based on progress
	if percent >= 1.0 {
		return successStyle.Render(bar)
	} else if percent >= 0.7 {
		return highlightStyle.Render(bar)
	} else if percent >= 0.4 {
		return warningStyle.Render(bar)
	} else {
		return infoStyle.Render(bar)
	}
}

// Legacy function for backward compatibility
func renderSimpleProgressBar(percent float64, width int) string {
	return renderProgressBar(percent, width, true)
}

// Render displays the current progress state
func (p *ProgressTracker) Render() {
	// Clear the current line and move cursor to beginning
	fmt.Print("\r\033[K")

	// Show title
	fmt.Printf("%s\n", headerStyle.Render(p.Title))

	// Show overall progress bar
	overallProgress := p.GetOverallProgress()
	progressBar := renderProgressBar(overallProgress, p.Width, true)
	fmt.Printf("%s\n", progressBar)

	// Show steps
	for i, step := range p.Steps {
		status := step.Status.String()
		name := step.Name

		if i == p.CurrentStep && step.Status == StatusInProgress {
			// Show spinner for current step
			if p.ShowSpinner {
				fmt.Printf("  %s %s %s\n",
					p.Spinner.View(),
					titleStyle.Render(name),
					infoStyle.Render(step.Description))
			} else {
				fmt.Printf("  %s %s %s\n",
					status,
					titleStyle.Render(name),
					infoStyle.Render(step.Description))
			}
		} else {
			// Regular step display
			style := infoStyle
			if step.Status == StatusCompleted {
				style = successStyle
			} else if step.Status == StatusFailed {
				style = errorStyle
			} else if step.Status == StatusSkipped {
				style = warningStyle
			}

			fmt.Printf("  %s %s", status, style.Render(name))
			if step.Description != "" {
				fmt.Printf(" %s", subtitleStyle.Render(step.Description))
			}
			fmt.Println()
		}
	}

	// Show elapsed time
	elapsed := time.Since(p.StartTime).Round(time.Second)
	fmt.Printf("\n%s %s\n",
		infoStyle.Render("Elapsed:"),
		successStyle.Render(elapsed.String()))
}

// Complete finishes the progress tracker
func (p *ProgressTracker) Complete() {
	// Mark any remaining steps as completed
	for i := range p.Steps {
		if p.Steps[i].Status == StatusPending || p.Steps[i].Status == StatusInProgress {
			p.Steps[i].Status = StatusCompleted
			p.Steps[i].EndTime = time.Now()
		}
	}

	if !p.Quiet {
		p.Render()

		// Show completion message
		elapsed := time.Since(p.StartTime).Round(time.Second)
		fmt.Printf("\n%s %s %s\n",
			successStyle.Render("✓"),
			successStyle.Render("All tasks completed successfully!"),
			infoStyle.Render(fmt.Sprintf("(in %s)", elapsed)))
	}
}

// Fail marks the tracker as failed
func (p *ProgressTracker) Fail(err error) {
	if !p.Quiet {
		p.Render()

		// Show failure message
		fmt.Printf("\n%s %s\n%s\n",
			errorStyle.Render("✗"),
			errorStyle.Render("Operation failed:"),
			errorStyle.Render(err.Error()))
	}
}

// Simple progress functions for quick use
func ShowSimpleProgress(message string, percent float64) {
	bar := renderProgressBar(percent, 50, true)
	fmt.Printf("\r%s %s", bar, message)
	if percent >= 1.0 {
		fmt.Println() // New line when complete
	}
}

// RunWithProgress runs a function with a simple progress bar
func RunWithProgress(message string, fn func() error) error {
	fmt.Printf("%s...", message)
	err := fn()
	if err != nil {
		fmt.Printf("\r%s %s ❌\n",
			renderProgressBar(0.0, 30, false),
			errorStyle.Render(message))
		return err
	}
	fmt.Printf("\r%s %s ✅\n",
		renderProgressBar(1.0, 30, false),
		successStyle.Render(message))
	return nil
}
