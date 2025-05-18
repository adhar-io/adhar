package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"adhar-io/adhar/platform/logger" // Corrected import path

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

const (
	// kindClusterName is the default name for the Kind cluster
	kindClusterName = "adhar"
)

var (
	// Platform flags for down command
	forceDelete bool
	verboseDown bool
	noAnimation bool
)

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
		return warningStyle.Render("Operation canceled") + "\nExiting...\n"
	}

	if m.err != nil {
		errorMessage := fmt.Sprintf("%s %s\n\n%s %s\n",
			errorStyle.Render("Error:"),
			m.err.Error(),
			errorStyle.Render("→"),
			"Failed to tear down Adhar platform")

		if strings.Contains(m.err.Error(), "cluster not found") {
			errorMessage += infoStyle.Render("\nNo cluster named 'adhar' exists. Nothing to tear down.")
		} else if strings.Contains(m.err.Error(), "permission") || strings.Contains(m.err.Error(), "access") {
			errorMessage += warningStyle.Render("\nTry running with sudo or with appropriate permissions.")
		} else if forceDelete {
			logger.Logger.Warn("Deletion failed even with --force. Check logs or perform manual cleanup.")
		} else {
			logger.Logger.Info("Deletion failed. Check logs or try manual cleanup if resources remain.")
		}

		return errorMessage
	}

	if m.done {
		successBox := borderStyle.Width(60).Render(
			fmt.Sprintf("%s %s\n\n%s\n",
				successStyle.Render("✓"),
				successStyle.Render("Successfully tore down Adhar platform!"),
				subtitleStyle.Render("Kind cluster and resources have been removed")))

		// Next steps
		nextSteps := fmt.Sprintf(`
%s
  → Run %s to start a new environment
  → Run %s to view the CLI version information
  → Run %s for more commands

%s %s
`,
			titleStyle.Render("Next Steps:"),
			highlightStyle.Render("adhar up"),
			highlightStyle.Render("adhar version"),
			highlightStyle.Render("adhar help"),
			infoStyle.Render("Teardown completed in:"),
			successStyle.Render(m.elapsedTime))

		return fmt.Sprintf("%s\n%s", successBox, nextSteps)
	}

	// In progress
	status := m.status
	if status == "" {
		status = "initializing..."
	}

	step := m.step
	if step == "" {
		step = "Preparing"
	}

	// Show the current spinner, step, and status
	view := fmt.Sprintf("\n%s %s %s",
		m.spinner.View(),
		headerStyle.Render(step),
		status)

	// Show elapsed time
	timeInfo := fmt.Sprintf("\n\n%s %s",
		infoStyle.Render("Elapsed time:"),
		m.elapsedTime)

	// Add extra info toggle hint
	toggleHint := subtitleStyle.Render("\nPress 'i' to toggle details")

	// Show extra output if toggled
	var extraInfo string
	if m.showExtraInfo && m.extraOutput != "" {
		extraInfo = fmt.Sprintf("\n\n%s\n%s",
			titleStyle.Render("Command Output:"),
			borderStyle.Render(m.extraOutput))
	}

	// Add a progress indicator
	mainContent := borderStyle.Width(60).Render(
		titleStyle.Render("Please wait while Adhar is tearing down your environment") +
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
		send(logger.StatusMsg("looking for cluster named '" + kindClusterName + "'"))

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
		send(logger.StatusMsg("removing '" + kindClusterName + "'"))

		deleteArgs := []string{"delete", "cluster", "--name", kindClusterName}

		deleteCmd := exec.Command("kind", deleteArgs...)
		output, err := deleteCmd.CombinedOutput()

		// Always capture output regardless of verbosity when there's an error
		if err != nil {
			send(logger.ExtraOutputMsg(string(output)))
			return logger.ErrorMsg{Err: fmt.Errorf("failed to delete cluster: %w", err)}
		}

		// Add the command output for verbose mode on success
		if verboseDown {
			send(logger.ExtraOutputMsg(string(output)))
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

	return strings.Contains(string(output), kindClusterName), nil
}

// updateElapsedTime creates a command that updates the elapsed time every second
func updateElapsedTime() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return logger.ElapsedTimeMsg(t)
	})
}

func init() {
	// Add the down command to the root command
	rootCmd.AddCommand(downCmd) // Corrected to rootCmd.AddCommand

	// Add flags for the down command
	downCmd.Flags().BoolVarP(&forceDelete, "force", "f", false, "Force deletion even if resources are still in use")
	downCmd.Flags().BoolVarP(&verboseDown, "verbose", "v", false, "Show detailed information during tear down")
	downCmd.Flags().BoolVar(&noAnimation, "no-animation", false, "Disable animations")
}

// downCmd represents the down command
var downCmd = &cobra.Command{
	Use:   "down",
	Short: "Tears down the local Kind cluster and cleans up Adhar resources",
	Long: `The 'down' command deletes the local Kubernetes cluster managed by Kind
named '` + kindClusterName + `' and removes all associated resources.
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

		s.Style = lipgloss.NewStyle().Foreground(primaryColor)

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
