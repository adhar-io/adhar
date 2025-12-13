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
	s.Style = HighlightStyle

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
	s.Style = HighlightStyle

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

// NewStyledProgressTracker creates a new progress tracker with styled single-box display
func NewStyledProgressTracker(title string, stepNames []string, descriptions []string) *ProgressTracker {
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
	s.Style = HighlightStyle

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

		// Start continuous spinner animation for smooth animation only if enabled
		if p.ShowSpinner {
			p.startSpinnerAnimation()
		}

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
		return SuccessStyle.Render(bar)
	} else if percent >= 0.8 {
		return HighlightStyle.Render(bar)
	} else if percent >= 0.5 {
		return WarningStyle.Render(bar)
	} else if percent >= 0.2 {
		return InfoStyle.Render(bar)
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
		return SuccessStyle.Render(bar)
	} else if percent >= 0.7 {
		return HighlightStyle.Render(bar)
	} else if percent >= 0.4 {
		return WarningStyle.Render(bar)
	} else {
		return InfoStyle.Render(bar)
	}
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

	// Print header only once; we never clear it
	if !p.headerRendered {
		fmt.Printf("🚀 %s\n", HeaderStyle.Render(p.Title))
		p.headerRendered = true
	}

	// Clear only the body we printed previously
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
		InfoStyle.Render(stepProgress),
		InfoStyle.Render("Elapsed: "+elapsedStr))
	lines++

	// Add spacing
	fmt.Println()
	lines++

	// Show all steps with their current status
	for _, step := range p.Steps {
		var statusIcon, stepText string

		switch step.Status {
		case StatusPending:
			statusIcon = InfoStyle.Render("⏳")
			stepText = InfoStyle.Render(step.Name)
		case StatusInProgress:
			statusIcon = HighlightStyle.Render(p.getCurrentSpinnerFrame())
			stepText = TitleStyle.Render(step.Name)
			if step.Description != "" {
				stepText += " " + SubtitleStyle.Render("- "+step.Description)
			}
		case StatusCompleted:
			statusIcon = SuccessStyle.Render("✓")
			stepText = SuccessStyle.Render(step.Name)
			if !step.EndTime.IsZero() && !step.StartTime.IsZero() {
				duration := step.EndTime.Sub(step.StartTime).Round(time.Millisecond)
				stepText += " " + InfoStyle.Render(fmt.Sprintf("(%s)", formatDuration(duration)))
			}
		case StatusFailed:
			statusIcon = ErrorStyle.Render("✗")
			stepText = ErrorStyle.Render(step.Name)
			if step.Error != nil {
				stepText += " " + ErrorStyle.Render("- "+step.Error.Error())
			}
		case StatusSkipped:
			statusIcon = WarningStyle.Render("⏭")
			stepText = WarningStyle.Render(step.Name)
			if step.Description != "" {
				stepText += " " + WarningStyle.Render("- "+step.Description)
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
		fmt.Printf("🚀 %s\n", HeaderStyle.Render(p.Title))
		p.headerRendered = true
		lines++
	}

	// Progress bar line - only print if header was rendered or if it's the first render
	if !p.headerRendered || p.linesPrinted == 0 {
		fmt.Printf("%s | %s %s\n",
			progressBar,
			InfoStyle.Render(fmt.Sprintf("(%d/%d)", min(p.CurrentStep+1, len(p.Steps)), len(p.Steps))),
			InfoStyle.Render("Elapsed: "+elapsedStr))
		lines++

		// Empty line for spacing
		fmt.Println()
		lines++
	} else {
		// Update progress bar in place without creating new lines
		fmt.Printf("\r%s | %s %s",
			progressBar,
			InfoStyle.Render(fmt.Sprintf("(%d/%d)", min(p.CurrentStep+1, len(p.Steps)), len(p.Steps))),
			InfoStyle.Render("Elapsed: "+elapsedStr))
		fmt.Printf("\n\n") // Move to the task list area
		lines += 2
	}

	// Show all steps with their current status
	for _, step := range p.Steps {
		var statusIcon, stepText string

		switch step.Status {
		case StatusPending:
			statusIcon = InfoStyle.Render("⏳")
			stepText = InfoStyle.Render(step.Name)
		case StatusInProgress:
			statusIcon = HighlightStyle.Render(p.getCurrentSpinnerFrame())
			stepText = TitleStyle.Render(step.Name)
			if step.Description != "" {
				stepText += " " + SubtitleStyle.Render("- "+step.Description)
			}
		case StatusCompleted:
			statusIcon = SuccessStyle.Render("✓")
			stepText = SuccessStyle.Render(step.Name)
			if !step.EndTime.IsZero() {
				duration := step.EndTime.Sub(step.StartTime).Round(time.Millisecond)
				stepText += " " + InfoStyle.Render(fmt.Sprintf("(%s)", formatDuration(duration)))
			}
		case StatusFailed:
			statusIcon = ErrorStyle.Render("✗")
			stepText = ErrorStyle.Render(step.Name)
			if step.Error != nil {
				stepText += " " + ErrorStyle.Render("- "+step.Error.Error())
			}
		case StatusSkipped:
			statusIcon = WarningStyle.Render("⏭")
			stepText = WarningStyle.Render(step.Name)
			if step.Description != "" {
				stepText += " " + WarningStyle.Render("- "+step.Description)
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
			SuccessStyle.Render("✓ All tasks completed"),
			SuccessStyle.Render(fmt.Sprintf("(%d/%d)", len(p.Steps), len(p.Steps))),
			InfoStyle.Render("Elapsed: "+elapsedStr))

		// Show all completed steps for final summary in a clean format
		fmt.Println()
		for _, step := range p.Steps {
			duration := ""
			if !step.EndTime.IsZero() && !step.StartTime.IsZero() {
				d := step.EndTime.Sub(step.StartTime).Round(time.Millisecond)
				duration = fmt.Sprintf(" (%s)", formatDuration(d))
			}
			fmt.Printf("  %s %s%s\n",
				SuccessStyle.Render("✓"),
				SuccessStyle.Render(step.Name),
				InfoStyle.Render(duration))
		}

		// Show detailed completion summary
		fmt.Printf("\n%s\n", SuccessStyle.Render("✅ Cluster Provisioning Complete!"))

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
				SuccessStyle.Render("✓"), completed)
		} else {
			fmt.Printf("   %s %d completed", SuccessStyle.Render("✓"), completed)
			if skipped > 0 {
				fmt.Printf(", %s %d skipped", WarningStyle.Render("⏭"), skipped)
			}
			if failed > 0 {
				fmt.Printf(", %s %d failed", ErrorStyle.Render("✗"), failed)
			}
			fmt.Println()
		}

		totalElapsed := time.Since(p.StartTime).Round(time.Second)
		totalElapsedStr := formatDuration(totalElapsed)
		fmt.Printf("   %s Total time: %s\n\n",
			InfoStyle.Render("⏱"),
			HighlightStyle.Render(totalElapsedStr))
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
			ErrorStyle.Render("✗ Operation failed"),
			ErrorStyle.Render(fmt.Sprintf("(%d/%d)", p.CurrentStep+1, len(p.Steps))),
			InfoStyle.Render("Elapsed: "+elapsedStr))

		// Show detailed failure message
		fmt.Printf("\n%s\n", ErrorStyle.Render("❌ Platform Setup Failed"))
		fmt.Printf("   %s Step: %s\n",
			ErrorStyle.Render("✗"),
			ErrorStyle.Render(p.Steps[p.CurrentStep].Name))
		fmt.Printf("   %s Error: %s\n",
			ErrorStyle.Render("⚠"),
			ErrorStyle.Render(err.Error()))

		// Show progress summary
		completed := 0
		for i := 0; i < p.CurrentStep; i++ {
			if p.Steps[i].Status == StatusCompleted {
				completed++
			}
		}

		if completed > 0 {
			fmt.Printf("   %s %d of %d steps completed before failure\n",
				InfoStyle.Render("ℹ"), completed, len(p.Steps))
		}

		fmt.Printf("   %s Total time: %s\n\n",
			InfoStyle.Render("⏱"),
			InfoStyle.Render(elapsedStr))
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
			ErrorStyle.Render(message))
		return err
	}
	fmt.Printf("\r%s %s ✅\n",
		renderProgressBar(1.0, 30, false),
		SuccessStyle.Render(message))
	return nil
}

// RenderStyledDisplay renders the current state in a single bordered box
func (pt *ProgressTracker) RenderStyledDisplay() {
	elapsed := time.Since(pt.StartTime).Round(time.Second)
	progressBar := pt.createStyledProgressBar()
	taskList := pt.createStyledTaskList()
	currentStatus := pt.getStyledCurrentStatus()

	mainContent := fmt.Sprintf("%s\n\n%s\n\n%s\n\n%s\n\n%s %s",
		TitleStyle.Render(pt.Title),
		progressBar,
		taskList,
		currentStatus,
		InfoStyle.Render("Elapsed time:"),
		elapsed)

	box := HighlightStyle.Width(80).Render(mainContent)
	fmt.Print("\r\033[K")
	fmt.Printf("\n%s\n", box)
}

// createStyledProgressBar creates a visual progress bar
func (pt *ProgressTracker) createStyledProgressBar() string {
	if len(pt.Steps) == 0 {
		return ""
	}

	completedSteps := 0
	for _, step := range pt.Steps {
		if step.Status == StatusCompleted {
			completedSteps++
		}
	}

	progress := float64(completedSteps) / float64(len(pt.Steps))
	barWidth := 60
	filled := int(float64(barWidth) * progress)
	bar := "["
	for i := 0; i < barWidth; i++ {
		if i < filled {
			bar += "█"
		} else {
			bar += "░"
		}
	}
	bar += "]"
	percentage := int(progress * 100)
	return fmt.Sprintf("%s %d%% (%d/%d steps)", bar, percentage, completedSteps, len(pt.Steps))
}

// createStyledTaskList creates a compact list of all tasks
func (pt *ProgressTracker) createStyledTaskList() string {
	if len(pt.Steps) == 0 {
		return ""
	}

	var taskList strings.Builder
	taskList.WriteString(TitleStyle.Render("Tasks:"))
	for _, step := range pt.Steps {
		status := pt.getStyledStatusIcon(step.Status)
		taskList.WriteString(fmt.Sprintf("\n  %s %s",
			status,
			SubtitleStyle.Render(step.Name)))
	}
	return taskList.String()
}

// getStyledCurrentStatus returns the current status information
func (pt *ProgressTracker) getStyledCurrentStatus() string {
	if pt.CurrentStep < 0 || pt.CurrentStep >= len(pt.Steps) {
		return InfoStyle.Render("Status: Initializing...")
	}

	step := pt.Steps[pt.CurrentStep]
	if step.Status == StatusInProgress {
		return fmt.Sprintf("%s %s\n%s",
			InfoStyle.Render("Status:"),
			SubtitleStyle.Render(fmt.Sprintf("Working on: %s", step.Name)),
			InfoStyle.Render(step.Description))
	}
	return fmt.Sprintf("%s %s",
		InfoStyle.Render("Status:"),
		SubtitleStyle.Render("Ready for next step"))
}

// getStyledStatusIcon returns the appropriate icon for the task status
func (pt *ProgressTracker) getStyledStatusIcon(status ProgressStatus) string {
	switch status {
	case StatusPending:
		return "⏳"
	case StatusInProgress:
		return "🔄"
	case StatusCompleted:
		return "✅"
	case StatusFailed:
		return "❌"
	case StatusSkipped:
		return "⚠️"
	default:
		return "⏳"
	}
}

// CompleteStyled marks all steps as completed and shows final message outside the box
func (pt *ProgressTracker) CompleteStyled() {
	elapsed := time.Since(pt.StartTime).Round(time.Second)

	// Mark all pending steps as completed
	for i := range pt.Steps {
		if pt.Steps[i].Status == StatusPending {
			pt.Steps[i].Status = StatusCompleted
		}
	}

	pt.RenderStyledDisplay()

	successBox := HighlightStyle.Width(60).Render(
		fmt.Sprintf("%s %s\n\n%s\n",
			SuccessStyle.Render("✓"),
			SuccessStyle.Render("Successfully set up Adhar platform!"),
			SubtitleStyle.Render("Your development environment is ready")))

	nextSteps := fmt.Sprintf(`
%s
  → Run %s to view platform status
  → Run %s to view applications
  → Run %s for more commands

%s %s
`,
		TitleStyle.Render("Next Steps:"),
		HighlightStyle.Render("adhar get"),
		HighlightStyle.Render("adhar apps"),
		HighlightStyle.Render("adhar help"),
		InfoStyle.Render("Setup completed in:"),
		SuccessStyle.Render(elapsed.String()))

	fmt.Printf("\n%s\n%s", successBox, nextSteps)
}
