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

// ProgressTracker manages progress tracking for long-running operations
type ProgressTracker struct {
	Title          string
	Steps          []ProgressStep
	CurrentStep    int
	StartTime      time.Time
	Width          int
	ShowSpinner    bool
	Quiet          bool
	ExpandedView   bool
	Spinner        spinner.Model
	spinnerIdx     int
	animating      bool
	stopChan       chan bool
	linesPrinted   int
	headerRendered bool // Flag to prevent duplicate headers
}

// NewProgressTrackerWithDetails creates a new progress tracker with detailed descriptions
func NewProgressTrackerWithDetails(title string, stepNames []string, descriptions []string) *ProgressTracker {
	steps := make([]ProgressStep, len(stepNames))
	for i, name := range stepNames {
		desc := ""
		if i < len(descriptions) {
			desc = descriptions[i]
		}
		steps[i] = ProgressStep{
			Name:        name,
			Description: desc,
			Status:      StatusPending,
		}
	}

	// Initialize spinner with enhanced frames for smooth animation
	s := spinner.New()
	s.Spinner = spinner.Spinner{
		Frames: []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"},
		FPS:    time.Millisecond * 100, // Smooth animation
	}
	s.Style = highlightStyle

	return &ProgressTracker{
		Title:          title,
		Steps:          steps,
		CurrentStep:    0,
		StartTime:      time.Now(),
		Width:          50,
		ShowSpinner:    true,
		Spinner:        s,
		Quiet:          false,
		spinnerIdx:     0,
		linesPrinted:   0,
		stopChan:       make(chan bool),
		animating:      false,
		ExpandedView:   false, // Use single line mode to prevent multiple progress bars
		headerRendered: false,
	}
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

	// Initialize spinner with enhanced frames for smooth animation
	s := spinner.New()
	s.Spinner = spinner.Spinner{
		Frames: []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"},
		FPS:    time.Millisecond * 100, // Smooth animation
	}
	s.Style = highlightStyle

	return &ProgressTracker{
		Title:          title,
		Steps:          steps,
		CurrentStep:    0,
		StartTime:      time.Now(),
		Width:          50,
		ShowSpinner:    true,
		Spinner:        s,
		Quiet:          false,
		spinnerIdx:     0,
		linesPrinted:   0,
		stopChan:       make(chan bool),
		animating:      false,
		ExpandedView:   false, // Use single line mode to prevent multiple progress bars
		headerRendered: false,
	}
}

// getCurrentSpinnerFrame returns the current spinner frame with animation
func (p *ProgressTracker) getCurrentSpinnerFrame() string {
	// Update spinner frame on each call for animation when rendering
	p.spinnerIdx = (p.spinnerIdx + 1) % len(p.Spinner.Spinner.Frames)
	return p.Spinner.Spinner.Frames[p.spinnerIdx]
}

// startSpinnerAnimation starts the continuous spinner animation
func (p *ProgressTracker) startSpinnerAnimation() {
	if p.animating {
		return // Already animating
	}

	p.animating = true
	go func() {
		ticker := time.NewTicker(200 * time.Millisecond) // Smooth animation but not too fast
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				if !p.Quiet && p.animating {
					// Update spinner frame and re-render only if there's an in-progress step
					p.updateSpinnerFrame()
					if p.hasInProgressStep() {
						p.renderSingleLine()
					}
				}
			case <-p.stopChan:
				return
			}
		}
	}()
}

// updateSpinnerFrame updates just the spinner frame without re-rendering
func (p *ProgressTracker) updateSpinnerFrame() {
	if p.CurrentStep < len(p.Steps) {
		step := p.Steps[p.CurrentStep]
		if step.Status == StatusInProgress {
			// Update spinner frame for current step only
			p.spinnerIdx = (p.spinnerIdx + 1) % len(p.Spinner.Spinner.Frames)
		}
	}
}

// hasInProgressStep checks if there are any steps currently in progress
func (p *ProgressTracker) hasInProgressStep() bool {
	for _, step := range p.Steps {
		if step.Status == StatusInProgress {
			return true
		}
	}
	return false
}

// stopSpinnerAnimation stops the continuous spinner animation
func (p *ProgressTracker) stopSpinnerAnimation() {
	if p.animating {
		p.animating = false
		close(p.stopChan)
		p.stopChan = make(chan bool) // Create new channel for next animation
	}
}

// StartStep marks a step as in progress
func (p *ProgressTracker) StartStep(stepIndex int, description string) {
	if stepIndex >= 0 && stepIndex < len(p.Steps) {
		p.Steps[stepIndex].Status = StatusInProgress
		if description != "" {
			p.Steps[stepIndex].Description = description
		}
		p.Steps[stepIndex].StartTime = time.Now()
		p.CurrentStep = stepIndex

		// Start continuous spinner animation for smooth animation
		p.startSpinnerAnimation()

		if !p.Quiet {
			if p.ExpandedView {
				p.renderExpandedView()
			} else {
				p.renderSingleLine()
			}
		}
	}
}

// ResetHeader resets the header rendered flag for new progress sessions
func (p *ProgressTracker) ResetHeader() {
	p.headerRendered = false
}

// CompleteStep marks a step as completed
func (p *ProgressTracker) CompleteStep(stepIndex int) {
	if stepIndex >= 0 && stepIndex < len(p.Steps) {
		p.Steps[stepIndex].Status = StatusCompleted
		p.Steps[stepIndex].EndTime = time.Now()

		// Stop continuous spinner animation when step completes
		p.stopSpinnerAnimation()

		if !p.Quiet {
			// Simply update the progress line without adding newlines
			p.renderSingleLine()
		}
	}
}

// FailStep marks a step as failed
func (p *ProgressTracker) FailStep(stepIndex int, err error) {
	if stepIndex >= 0 && stepIndex < len(p.Steps) {
		p.Steps[stepIndex].Status = StatusFailed
		p.Steps[stepIndex].Error = err
		p.Steps[stepIndex].EndTime = time.Now()

		// Stop spinner animation on failure
		p.stopSpinnerAnimation()

		if !p.Quiet {
			if p.ExpandedView {
				p.renderExpandedView()
			} else {
				p.renderSingleLine()
			}
		}
	}
}

// SkipStep marks a step as skipped
func (p *ProgressTracker) SkipStep(stepIndex int, reason string) {
	if stepIndex >= 0 && stepIndex < len(p.Steps) {
		p.Steps[stepIndex].Status = StatusSkipped
		p.Steps[stepIndex].Description = reason
		p.Steps[stepIndex].EndTime = time.Now()

		// Stop spinner animation when skipping
		p.stopSpinnerAnimation()

		if !p.Quiet {
			if p.ExpandedView {
				p.renderExpandedView()
			} else {
				p.renderSingleLine()
			}
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

// renderEnhancedProgressBar renders a more visually appealing progress bar
func renderEnhancedProgressBar(percent float64, width int) string {
	w := width - 7 // Account for brackets and percentage
	fill := int(percent * float64(w))
	empty := w - fill

	// Use different characters for a more modern look
	var fillChar, emptyChar string
	if percent >= 1.0 {
		fillChar = "█"
		emptyChar = ""
	} else {
		fillChar = "█"
		emptyChar = "░"
	}

	bar := fmt.Sprintf("[%s%s] %3.0f%%",
		strings.Repeat(fillChar, fill),
		strings.Repeat(emptyChar, empty),
		percent*100)

	// Color the bar based on progress with enhanced styling
	if percent >= 1.0 {
		return successStyle.Render(bar)
	} else if percent >= 0.8 {
		return highlightStyle.Render(bar)
	} else if percent >= 0.5 {
		return warningStyle.Render(bar)
	} else if percent >= 0.2 {
		return infoStyle.Render(bar)
	} else {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render(bar)
	}
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

// formatDuration formats duration in a user-friendly way
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	} else if d < time.Hour {
		minutes := int(d.Minutes())
		seconds := int(d.Seconds()) % 60
		return fmt.Sprintf("%dm%ds", minutes, seconds)
	} else {
		hours := int(d.Hours())
		minutes := int(d.Minutes()) % 60
		return fmt.Sprintf("%dh%dm", hours, minutes)
	}
}

// min returns the minimum of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// renderSingleLine displays a clean progress with individual task statuses
func (p *ProgressTracker) renderSingleLine() {
	if p.Quiet {
		return
	}

	// Only render header once to prevent duplicates
	if !p.headerRendered {
		fmt.Printf("🚀 %s\n", headerStyle.Render(p.Title))
		p.headerRendered = true
	}

	// Clear previous lines if we have printed before
	if p.linesPrinted > 0 {
		p.clearLines(p.linesPrinted)
	}

	lines := 0

	// Show enhanced progress with better visual feedback
	overallProgress := p.GetOverallProgress()
	progressBar := renderEnhancedProgressBar(overallProgress, 50)

	// Show elapsed time with better formatting
	elapsed := time.Since(p.StartTime).Round(time.Second)
	elapsedStr := formatDuration(elapsed)

	// Show step progress (current/total)
	stepProgress := fmt.Sprintf("(%d/%d)",
		min(p.CurrentStep+1, len(p.Steps)),
		len(p.Steps))

	// Show overall progress line
	fmt.Printf("%s | %s %s\n",
		progressBar,
		infoStyle.Render(stepProgress),
		infoStyle.Render("Elapsed: "+elapsedStr))
	lines++

	// Add spacing
	fmt.Println()
	lines++

	// Show all steps with their current status
	for _, step := range p.Steps {
		var statusIcon, stepText string

		switch step.Status {
		case StatusPending:
			statusIcon = infoStyle.Render("⏳")
			stepText = infoStyle.Render(step.Name)
		case StatusInProgress:
			statusIcon = highlightStyle.Render(p.getCurrentSpinnerFrame())
			stepText = titleStyle.Render(step.Name)
			if step.Description != "" {
				stepText += " " + subtitleStyle.Render("- "+step.Description)
			}
		case StatusCompleted:
			statusIcon = successStyle.Render("✓")
			stepText = successStyle.Render(step.Name)
			if !step.EndTime.IsZero() && !step.StartTime.IsZero() {
				duration := step.EndTime.Sub(step.StartTime).Round(time.Millisecond)
				stepText += " " + infoStyle.Render(fmt.Sprintf("(%s)", formatDuration(duration)))
			}
		case StatusFailed:
			statusIcon = errorStyle.Render("✗")
			stepText = errorStyle.Render(step.Name)
			if step.Error != nil {
				stepText += " " + errorStyle.Render("- "+step.Error.Error())
			}
		case StatusSkipped:
			statusIcon = warningStyle.Render("⏭")
			stepText = warningStyle.Render(step.Name)
			if step.Description != "" {
				stepText += " " + warningStyle.Render("- "+step.Description)
			}
		}

		fmt.Printf("  %s %s\n", statusIcon, stepText)
		lines++
	}

	// Store the number of lines we printed for next clear operation
	p.linesPrinted = lines
}

// clearLines clears the specified number of lines from the terminal
func (p *ProgressTracker) clearLines(numLines int) {
	for i := 0; i < numLines; i++ {
		fmt.Print("\033[1A\033[K") // Move up one line and clear it
	}
}

// renderExpandedView displays all tasks in an expanded list format with individual spinners
func (p *ProgressTracker) renderExpandedView() {
	if p.Quiet {
		return
	}

	// Clear previous output if we've printed lines before
	if p.linesPrinted > 0 {
		p.clearLines(p.linesPrinted)
	}

	lines := 0

	// Show title and overall progress
	overallProgress := p.GetOverallProgress()
	progressBar := renderEnhancedProgressBar(overallProgress, 60)
	elapsed := time.Since(p.StartTime).Round(time.Second)
	elapsedStr := formatDuration(elapsed)

	// Header line - only render once
	if !p.headerRendered {
		fmt.Printf("🚀 %s\n", headerStyle.Render(p.Title))
		p.headerRendered = true
		lines++
	}

	// Progress bar line - only print if header was rendered or if it's the first render
	if !p.headerRendered || p.linesPrinted == 0 {
		fmt.Printf("%s | %s %s\n",
			progressBar,
			infoStyle.Render(fmt.Sprintf("(%d/%d)", min(p.CurrentStep+1, len(p.Steps)), len(p.Steps))),
			infoStyle.Render("Elapsed: "+elapsedStr))
		lines++

		// Empty line for spacing
		fmt.Println()
		lines++
	} else {
		// Update progress bar in place without creating new lines
		fmt.Printf("\r%s | %s %s",
			progressBar,
			infoStyle.Render(fmt.Sprintf("(%d/%d)", min(p.CurrentStep+1, len(p.Steps)), len(p.Steps))),
			infoStyle.Render("Elapsed: "+elapsedStr))
		fmt.Printf("\n\n") // Move to the task list area
		lines += 2
	}

	// Show all steps with their current status
	for _, step := range p.Steps {
		var statusIcon, stepText string

		switch step.Status {
		case StatusPending:
			statusIcon = infoStyle.Render("⏳")
			stepText = infoStyle.Render(step.Name)
		case StatusInProgress:
			statusIcon = highlightStyle.Render(p.getCurrentSpinnerFrame())
			stepText = titleStyle.Render(step.Name)
			if step.Description != "" {
				stepText += " " + subtitleStyle.Render("- "+step.Description)
			}
		case StatusCompleted:
			statusIcon = successStyle.Render("✓")
			stepText = successStyle.Render(step.Name)
			if !step.EndTime.IsZero() {
				duration := step.EndTime.Sub(step.StartTime).Round(time.Millisecond)
				stepText += " " + infoStyle.Render(fmt.Sprintf("(%s)", formatDuration(duration)))
			}
		case StatusFailed:
			statusIcon = errorStyle.Render("✗")
			stepText = errorStyle.Render(step.Name)
			if step.Error != nil {
				stepText += " " + errorStyle.Render("- "+step.Error.Error())
			}
		case StatusSkipped:
			statusIcon = warningStyle.Render("⏭")
			stepText = warningStyle.Render(step.Name)
			if step.Description != "" {
				stepText += " " + warningStyle.Render("- "+step.Description)
			}
		}

		fmt.Printf("  %s %s\n", statusIcon, stepText)
		lines++
	}

	// Store the number of lines we printed for next clear operation
	p.linesPrinted = lines
}

// Refresh updates the display with current spinner state
func (p *ProgressTracker) Refresh() {
	if !p.Quiet {
		if p.ExpandedView {
			p.renderExpandedView()
		} else {
			p.renderSingleLine()
		}
	}
}

// Render displays the current progress state
func (p *ProgressTracker) Render() {
	if p.ExpandedView {
		p.renderExpandedView()
	} else {
		p.renderSingleLine()
	}
}

// Complete finishes the progress tracker with enhanced completion feedback
func (p *ProgressTracker) Complete() {
	// Stop any ongoing spinner animation
	p.stopSpinnerAnimation()

	// Mark any remaining steps as completed
	for i := range p.Steps {
		if p.Steps[i].Status == StatusPending || p.Steps[i].Status == StatusInProgress {
			p.Steps[i].Status = StatusCompleted
			p.Steps[i].EndTime = time.Now()
		}
	}

	if !p.Quiet {
		// Clear current line and show final progress
		fmt.Print("\r\033[K")

		// Show 100% completion bar
		finalBar := renderEnhancedProgressBar(1.0, 50)
		elapsed := time.Since(p.StartTime).Round(time.Second)
		elapsedStr := formatDuration(elapsed)

		// Show final completion line
		fmt.Printf("  %s %s | %s %s\n",
			finalBar,
			successStyle.Render("✓ All tasks completed"),
			successStyle.Render(fmt.Sprintf("(%d/%d)", len(p.Steps), len(p.Steps))),
			infoStyle.Render("Elapsed: "+elapsedStr))

		// Show all completed steps for final summary in a clean format
		fmt.Println()
		for _, step := range p.Steps {
			duration := ""
			if !step.EndTime.IsZero() && !step.StartTime.IsZero() {
				d := step.EndTime.Sub(step.StartTime).Round(time.Millisecond)
				duration = fmt.Sprintf(" (%s)", formatDuration(d))
			}
			fmt.Printf("  %s %s%s\n",
				successStyle.Render("✓"),
				successStyle.Render(step.Name),
				infoStyle.Render(duration))
		}

		// Show detailed completion summary
		fmt.Printf("\n%s\n", successStyle.Render("✅ Cluster Provisioning Complete!"))

		// Count completed, failed, and skipped steps
		completed, failed, skipped := 0, 0, 0
		for _, step := range p.Steps {
			switch step.Status {
			case StatusCompleted:
				completed++
			case StatusFailed:
				failed++
			case StatusSkipped:
				skipped++
			}
		}

		// Show summary
		if failed == 0 && skipped == 0 {
			fmt.Printf("   %s All %d steps completed successfully\n",
				successStyle.Render("✓"), completed)
		} else {
			fmt.Printf("   %s %d completed", successStyle.Render("✓"), completed)
			if skipped > 0 {
				fmt.Printf(", %s %d skipped", warningStyle.Render("⏭"), skipped)
			}
			if failed > 0 {
				fmt.Printf(", %s %d failed", errorStyle.Render("✗"), failed)
			}
			fmt.Println()
		}

		totalElapsed := time.Since(p.StartTime).Round(time.Second)
		totalElapsedStr := formatDuration(totalElapsed)
		fmt.Printf("   %s Total time: %s\n\n",
			infoStyle.Render("⏱"),
			highlightStyle.Render(totalElapsedStr))
	}
}

// Fail marks the tracker as failed with enhanced error feedback
func (p *ProgressTracker) Fail(err error) {
	// Stop any ongoing spinner animation
	p.stopSpinnerAnimation()

	if !p.Quiet {
		// Clear current line
		fmt.Print("\r\033[K")

		// Show current progress with failure indication
		overallProgress := p.GetOverallProgress()
		failedBar := renderEnhancedProgressBar(overallProgress, 50)
		elapsed := time.Since(p.StartTime).Round(time.Second)
		elapsedStr := formatDuration(elapsed)

		// Show failure line
		fmt.Printf("🚀 Setting up Adhar Platform %s %s | %s %s\n",
			failedBar,
			errorStyle.Render("✗ Operation failed"),
			errorStyle.Render(fmt.Sprintf("(%d/%d)", p.CurrentStep+1, len(p.Steps))),
			infoStyle.Render("Elapsed: "+elapsedStr))

		// Show detailed failure message
		fmt.Printf("\n%s\n", errorStyle.Render("❌ Platform Setup Failed"))
		fmt.Printf("   %s Step: %s\n",
			errorStyle.Render("✗"),
			errorStyle.Render(p.Steps[p.CurrentStep].Name))
		fmt.Printf("   %s Error: %s\n",
			errorStyle.Render("⚠"),
			errorStyle.Render(err.Error()))

		// Show progress summary
		completed := 0
		for i := 0; i < p.CurrentStep; i++ {
			if p.Steps[i].Status == StatusCompleted {
				completed++
			}
		}

		if completed > 0 {
			fmt.Printf("   %s %d of %d steps completed before failure\n",
				infoStyle.Render("ℹ"), completed, len(p.Steps))
		}

		fmt.Printf("   %s Total time: %s\n\n",
			infoStyle.Render("⏱"),
			infoStyle.Render(elapsedStr))
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
