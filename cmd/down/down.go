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

package down

import (
	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/logger"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

// DownCmd represents the down command
var DownCmd = &cobra.Command{
	Use:   "down",
	Short: "Tears down the local Kind cluster and cleans up Adhar resources",
	Long: `The 'down' command deletes the local Kubernetes cluster managed by Kind
named '` + globals.DefaultClusterName + `' and removes all associated resources.
This is useful for cleanup or resetting your development environment.

During execution:
- Press 'i' to toggle detailed output
- Press Ctrl+C to cancel the operation

Examples:
  # Tear down the local environment
  adhar down

  # Force the tear down even if resources are still in use
  adhar down --force

  # Show detailed information during tear down
  adhar down --verbose`,
	Run: func(cmd *cobra.Command, args []string) {
		// Initialize spinner model
		s := spinner.New()

		// Use a more interesting spinner if animations are enabled
		if !noAnimation {
			s.Spinner = spinner.Spinner{
				Frames: []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"},
				FPS:    10,
			}
		} else {
			s.Spinner = spinner.Dot
		}

		s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("#8b5cf6"))

		// Initialize model. --verbose starts with the detail pane already expanded
		// (users can still toggle it live with 'i').
		m := downModel{
			spinner:       s,
			startTime:     time.Now(),
			sub:           make(chan tea.Msg),
			showExtraInfo: verboseDown,
		}

		// Initialize Bubble Tea program
		p := tea.NewProgram(m)

		// Run the UI
		if _, err := p.Run(); err != nil {
			fmt.Println("Error running program:", err)
			os.Exit(1)
		}
	},
}

var (
	// Platform flags for down command
	forceDelete bool
	verboseDown bool
	noAnimation bool
)

func init() {
	// Add flags for the down command
	DownCmd.Flags().BoolVarP(&forceDelete, "force", "f", false, "Force deletion even if resources are still in use")
	DownCmd.Flags().BoolVarP(&verboseDown, "verbose", "v", false, "Show detailed information during tear down")
	DownCmd.Flags().BoolVar(&noAnimation, "no-animation", false, "Disable animations")
}

// downModel is the Bubble Tea model for the down command
type downModel struct {
	spinner       spinner.Model
	step          string
	status        string
	done          bool
	err           error
	quitting      bool
	startTime     time.Time
	elapsedTime   string
	outputLines   []string // accumulated detail lines (shown when toggled with 'i')
	showExtraInfo bool
	sub           chan tea.Msg // teardown goroutine -> UI message stream
}

// maxDetailLines caps how many detail lines are retained/shown so the pane
// doesn't grow unbounded during teardown.
const maxDetailLines = 200

// Init implements tea.Model
func (m downModel) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
		startClusterTeardown(m.sub), // runs the teardown, streaming progress into m.sub
		listenForActivity(m.sub),    // pumps streamed messages into the Update loop
		updateElapsedTime(),
	)
}

// Update implements tea.Model
func (m downModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		case "i":
			// Toggle extra info
			m.showExtraInfo = !m.showExtraInfo
			return m, nil
		}
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case logger.StepMsg:
		m.step = string(msg)
		return m, listenForActivity(m.sub)

	case logger.StatusMsg:
		m.status = string(msg)
		return m, listenForActivity(m.sub)

	case logger.ExtraOutputMsg:
		// Append each streamed detail line (splitting on newlines) and cap the
		// retained history so the toggled pane stays bounded.
		for _, line := range strings.Split(string(msg), "\n") {
			m.outputLines = append(m.outputLines, line)
		}
		if len(m.outputLines) > maxDetailLines {
			m.outputLines = m.outputLines[len(m.outputLines)-maxDetailLines:]
		}
		return m, listenForActivity(m.sub)

	case logger.ErrorMsg:
		m.err = msg.Err
		m.done = true
		return m, tea.Quit

	case logger.DoneMsg:
		m.done = true
		return m, tea.Quit

	case logger.ElapsedTimeMsg:
		// Use String() method for duration formatting
		m.elapsedTime = time.Since(m.startTime).Round(time.Second).String()
		return m, updateElapsedTime()

	default:
		return m, nil
	}
}

// View implements tea.Model
func (m downModel) View() string {
	if m.quitting {
		return helpers.WarningStyle.Render("Operation canceled") + "\nExiting...\n"
	}

	if m.err != nil {
		errorMessage := fmt.Sprintf("%s %s\n\n%s %s\n",
			helpers.ErrorStyle.Render("Error:"),
			m.err.Error(),
			helpers.ErrorStyle.Render("→"),
			"Failed to tear down Adhar platform")

		if strings.Contains(m.err.Error(), "cluster not found") {
			errorMessage += helpers.InfoStyle.Render("\nNo cluster named '" + globals.DefaultClusterName + "' exists. Nothing to tear down.")
		} else if strings.Contains(m.err.Error(), "permission") || strings.Contains(m.err.Error(), "access") {
			errorMessage += helpers.WarningStyle.Render("\nTry running with sudo or with appropriate permissions.")
		} else if forceDelete {
			logger.GetLogger().Warn("Deletion failed even with --force. Check logs or perform manual cleanup.")
		} else {
			logger.GetLogger().Info("Deletion failed. Check logs or try manual cleanup if resources remain.")
		}

		return errorMessage
	}

	if m.done {
		successBox := helpers.BorderStyle.Width(60).Render(
			fmt.Sprintf("%s %s\n\n%s\n",
				helpers.SuccessStyle.Render("✓"),
				helpers.SuccessStyle.Render("Successfully tore down Adhar platform!"),
				helpers.SubtitleStyle.Render("Kind cluster and resources have been removed")))

		// Next steps
		nextSteps := fmt.Sprintf(`
%s
  → Run %s to start a new environment
  → Run %s to view the CLI version information
  → Run %s for more commands

%s %s
`,
			helpers.TitleStyle.Render("Next Steps:"),
			helpers.HighlightStyle.Render("adhar up"),
			helpers.HighlightStyle.Render("adhar version"),
			helpers.HighlightStyle.Render("adhar help"),
			helpers.InfoStyle.Render("Teardown completed in:"),
			helpers.SuccessStyle.Render(m.elapsedTime))

		return fmt.Sprintf("%s\n%s", successBox, nextSteps)
	}

	// In progress
	status := m.status
	if status == "" {
		status = "Cleaning up..."
	}

	step := m.step
	if step == "" {
		step = "Working"
	}

	// Show the current spinner, step, and status
	view := fmt.Sprintf("\n%s %s %s",
		m.spinner.View(),
		helpers.TitleStyle.Render(step),
		status)

	// Show elapsed time
	timeInfo := fmt.Sprintf("\n\n%s %s",
		helpers.InfoStyle.Render("Elapsed time:"),
		m.elapsedTime)

	// Add extra info toggle hint (reflects current state)
	hintLabel := "Press 'i' to show details"
	if m.showExtraInfo {
		hintLabel = "Press 'i' to hide details"
	}
	toggleHint := helpers.SubtitleStyle.Render("\n" + hintLabel)

	// Show streamed command output if toggled on
	var extraInfo string
	if m.showExtraInfo {
		detail := strings.Join(m.outputLines, "\n")
		if strings.TrimSpace(detail) == "" {
			detail = "(waiting for output…)"
		}
		extraInfo = fmt.Sprintf("\n\n%s\n%s",
			helpers.TitleStyle.Render("Command Output:"),
			helpers.BorderStyle.Render(detail))
	}

	// Add a progress indicator
	mainContent := helpers.BorderStyle.Width(60).Render(
		helpers.TitleStyle.Render("Please wait while Adhar is tearing down your environment") +
			"\n\n" + view + timeInfo + toggleHint + extraInfo)

	return fmt.Sprintf("\n%s\n", mainContent)
}

// listenForActivity returns a command that blocks until the teardown goroutine
// emits its next message, then delivers it to the Update loop. The loop re-issues
// this command after each streamed message so progress flows continuously.
func listenForActivity(sub chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		return <-sub
	}
}

// startClusterTeardown launches the teardown in a background goroutine that
// streams step/status/detail messages onto sub. It returns immediately so the
// UI stays responsive (spinner, elapsed time, and the 'i' details toggle).
func startClusterTeardown(sub chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		go teardown(sub)
		return nil
	}
}

// teardown performs the cluster deletion, emitting progress and detailed command
// output onto sub. The detail lines are what the 'i' toggle reveals.
func teardown(sub chan tea.Msg) {
	emit := func(m tea.Msg) { sub <- m }
	detail := func(format string, a ...interface{}) {
		sub <- logger.ExtraOutputMsg(fmt.Sprintf(format, a...))
	}
	// emitCmdOutput streams a command's combined output line-by-line into detail.
	emitCmdOutput := func(out []byte) {
		for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
			if strings.TrimSpace(line) != "" {
				detail("  %s", line)
			}
		}
	}

	// Step 1: Check if the Kind cluster exists
	emit(logger.StepMsg("Checking cluster"))
	emit(logger.StatusMsg("Verifying Docker and Kind..."))
	detail("→ Checking Docker daemon and Kind availability")
	exists, err := kindClusterExists()
	if err != nil {
		detail("✗ %v", err)
		emit(logger.ErrorMsg{Err: fmt.Errorf("failed to check if cluster exists: %w", err)})
		return
	}
	if !exists {
		detail("✗ No cluster named '%s' found", globals.DefaultClusterName)
		emit(logger.ErrorMsg{Err: fmt.Errorf("no cluster named '%s' exists. Nothing to tear down", globals.DefaultClusterName)})
		return
	}
	detail("✓ Found a cluster to tear down")

	// Step 2: Delete the Kind cluster (with timeout)
	emit(logger.StepMsg("Deleting cluster"))
	clusterNames := []string{globals.DefaultClusterName, "adhar-local"}
	deleted := false

	for _, clusterName := range clusterNames {
		emit(logger.StatusMsg(fmt.Sprintf("Deleting Kind cluster '%s'...", clusterName)))
		detail("→ kind delete cluster --name %s", clusterName)

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		deleteCmd := exec.CommandContext(ctx, "kind", "delete", "cluster", "--name", clusterName)
		output, err := deleteCmd.CombinedOutput()
		cancel()
		emitCmdOutput(output)

		if err == nil {
			deleted = true
			detail("✓ Deleted cluster '%s'", clusterName)
			break
		}
		// Fallback: force-remove docker containers for this cluster
		detail("! kind delete failed for '%s' (%v) — removing containers directly", clusterName, err)
		for _, suffix := range []string{"-control-plane", "-worker", "-worker2"} {
			_ = exec.Command("docker", "rm", "-f", clusterName+suffix).Run()
		}
		detail("  removed any leftover '%s' docker containers", clusterName)
	}

	// Step 3: Clean up docker network
	emit(logger.StepMsg("Cleaning up"))
	emit(logger.StatusMsg("Removing the 'kind' docker network..."))
	detail("→ docker network rm kind")
	_ = exec.Command("docker", "network", "rm", "kind").Run()

	if !deleted {
		// Check if containers were at least removed via the fallback
		out, _ := exec.Command("docker", "ps", "-a", "--filter", "name=adhar", "--format", "{{.Names}}").CombinedOutput()
		if strings.TrimSpace(string(out)) == "" {
			deleted = true // Containers gone via fallback cleanup
			detail("✓ No adhar containers remain")
		}
	}

	if !deleted {
		emit(logger.ErrorMsg{Err: fmt.Errorf("failed to delete cluster. Tried: %v", clusterNames)})
		return
	}

	// Step 4: Clean up leftover files
	emit(logger.StatusMsg("Removing leftover kubeconfig files..."))
	removed := cleanupFiles()
	if len(removed) == 0 {
		detail("→ No leftover kubeconfig files to remove")
	} else {
		for _, f := range removed {
			detail("  removed %s", f)
		}
	}

	emit(logger.StatusMsg("Teardown complete"))
	detail("✓ Teardown complete")
	emit(logger.DoneMsg{})
}

// cleanupFiles removes any leftover kubeconfig files generated during 'up' and
// returns the paths it removed (for the detailed teardown output).
func cleanupFiles() []string {
	patterns := []string{"*-kubeconfig.yaml"}
	var removed []string

	remove := func(dir string) {
		for _, pattern := range patterns {
			glob := pattern
			if dir != "" {
				glob = filepath.Join(dir, pattern)
			}
			if files, err := filepath.Glob(glob); err == nil {
				for _, file := range files {
					if os.Remove(file) == nil {
						removed = append(removed, file)
					}
				}
			}
		}
	}

	// Search home directory, then the current directory.
	if home, err := os.UserHomeDir(); err == nil {
		remove(home)
	}
	remove("")

	return removed
}

// kindClusterExists checks if the Kind cluster exists and verifies Docker is running
func kindClusterExists() (bool, error) {
	// First check if Docker is running
	dockerCmd := exec.Command("docker", "info")
	if err := dockerCmd.Run(); err != nil {
		return false, fmt.Errorf("docker is not running or not accessible. Please start Docker before checking for Kind clusters")
	}

	// Check if kind executable exists
	_, err := exec.LookPath("kind")
	if err != nil {
		return false, fmt.Errorf("kind command not found in PATH. Please install kind: https://kind.sigs.k8s.io/docs/user/quick-start/#installation")
	}

	cmd := exec.Command("kind", "get", "clusters")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("failed to run 'kind get clusters': %w\nOutput: %s", err, string(output))
	}

	// Check for both possible cluster names (for backward compatibility)
	clusterOutput := string(output)
	if strings.Contains(clusterOutput, globals.DefaultClusterName) || strings.Contains(clusterOutput, "adhar-local") {
		return true, nil
	}

	return false, nil
}

// updateElapsedTime creates a command that updates the elapsed time every second
func updateElapsedTime() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return logger.ElapsedTimeMsg(t.Format("15:04:05"))
	})
}
