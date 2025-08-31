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

		// Initialize model
		m := downModel{
			spinner:   s,
			startTime: time.Now(),
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
	extraOutput   string
	showExtraInfo bool
}

// Init implements tea.Model
func (m downModel) Init() tea.Cmd {
	// Record the start time for tracking elapsed time
	m.startTime = time.Now()

	return tea.Batch(
		m.spinner.Tick,
		startClusterTeardown(),
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
		return m, nil

	case logger.StatusMsg:
		m.status = string(msg)
		return m, nil

	case logger.ExtraOutputMsg:
		m.extraOutput = string(msg)
		return m, nil

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

	// Add extra info toggle hint
	toggleHint := helpers.SubtitleStyle.Render("\nPress 'i' to toggle details")

	// Show extra output if toggled
	var extraInfo string
	if m.showExtraInfo && m.extraOutput != "" {
		extraInfo = fmt.Sprintf("\n\n%s\n%s",
			helpers.TitleStyle.Render("Command Output:"),
			helpers.BorderStyle.Render(m.extraOutput))
	}

	// Add a progress indicator
	mainContent := helpers.BorderStyle.Width(60).Render(
		helpers.TitleStyle.Render("Please wait while Adhar is tearing down your environment") +
			"\n\n" + view + timeInfo + toggleHint + extraInfo)

	return fmt.Sprintf("\n%s\n", mainContent)
}

// send is a helper function to send messages to the Bubble Tea program
func send(msg tea.Msg) tea.Cmd {
	return func() tea.Msg {
		return msg
	}
}

// startClusterTeardown starts the asynchronous operation to tear down the cluster
func startClusterTeardown() tea.Cmd {
	return func() tea.Msg {
		// Check if the Kind cluster exists
		send(logger.StepMsg("Checking for Kind cluster"))
		send(logger.StatusMsg("looking for cluster named '" + globals.DefaultClusterName + "'"))

		exists, err := kindClusterExists()
		if err != nil {
			return logger.ErrorMsg{Err: fmt.Errorf("failed to check if cluster exists: %w", err)}
		}

		if !exists {
			return logger.ErrorMsg{Err: fmt.Errorf("cluster not found")}
		}

		// Get active resources before deleting for verbose output
		if verboseDown {
			send(logger.StepMsg("Getting cluster resources"))
			send(logger.StatusMsg("collecting information"))

			// Run kubectl to get resources
			cmd := exec.Command("kubectl", "get", "all", "--all-namespaces")
			output, err := cmd.CombinedOutput()
			if err != nil {
				// Send a warning if kubectl fails, but don't stop the teardown
				send(logger.ExtraOutputMsg(fmt.Sprintf("Warning: Failed to get resources before deletion: %s\nOutput:\n%s", err, string(output))))
			} else {
				send(logger.ExtraOutputMsg(fmt.Sprintf("Resources before deletion:\n%s", string(output))))
			}
		}

		// Delete the Kind cluster
		send(logger.StepMsg("Deleting Kind cluster"))
		send(logger.StatusMsg("removing '" + globals.DefaultClusterName + "'"))

		// Try to delete both possible cluster names (for backward compatibility)
		clusterNames := []string{globals.DefaultClusterName, "adhar-local"}
		var deleteOutput string

		for _, clusterName := range clusterNames {
			deleteArgs := []string{"delete", "cluster", "--name", clusterName}
			deleteCmd := exec.Command("kind", deleteArgs...)
			output, err := deleteCmd.CombinedOutput()

			if err == nil {
				// Successfully deleted a cluster
				deleteOutput = string(output)
				break
			} else {
				// Try the next cluster name
				continue
			}
		}

		// If we couldn't delete any cluster, return an error
		if deleteOutput == "" {
			return logger.ErrorMsg{Err: fmt.Errorf("failed to delete any cluster. Tried: %v", clusterNames)}
		}

		// Add the command output for verbose mode on success
		if verboseDown {
			send(logger.ExtraOutputMsg(deleteOutput))
		}

		// Remove any cluster-related files
		cleanupFiles()

		return logger.DoneMsg{}
	}
}

// cleanupFiles removes any leftover files related to the cluster
func cleanupFiles() {
	send(logger.StepMsg("Cleaning up files"))
	send(logger.StatusMsg("removing leftover files"))

	// Try to find and remove any kubeconfig files generated during 'up'
	home, err := os.UserHomeDir()
	if err == nil {
		files, err := filepath.Glob(filepath.Join(home, "*-kubeconfig.yaml"))
		if err == nil {
			for _, file := range files {
				if verboseDown {
					send(logger.ExtraOutputMsg(fmt.Sprintf("Removing file: %s", file)))
				}
				os.Remove(file)
			}
		}
	}

	// Current directory kubeconfig files
	files, err := filepath.Glob("*-kubeconfig.yaml")
	if err == nil {
		for _, file := range files {
			if verboseDown {
				send(logger.ExtraOutputMsg(fmt.Sprintf("Removing file: %s", file)))
			}
			os.Remove(file)
		}
	}
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
