package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"adhar-io/adhar/platform/logger"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
)

const (
	kindClusterName = "adhar"
)

var (
	waitForReadiness    bool
	timeout             int
	kubeconfigNamespace string
	noSpinner           bool
	verboseUp           bool
	environmentName     string
)

type upModel struct {
	spinner     spinner.Model
	status      string
	done        bool
	err         error
	quitting    bool
	startTime   time.Time
	elapsedTime string
}

func (m *upModel) Init() tea.Cmd {
	m.startTime = time.Now()
	return tea.Batch(m.spinner.Tick, updateElapsedTime())
}

func (m *upModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			m.quitting = true
			return m, tea.Quit
		}
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case logger.StatusMsg:
		m.status = string(msg)
		return m, nil

	case logger.ErrorMsg:
		m.err = msg.Err
		m.done = true
		return m, tea.Quit

	case logger.DoneMsg:
		m.done = true
		return m, tea.Quit

	case logger.ElapsedTimeMsg:
		m.elapsedTime = time.Since(m.startTime).Round(time.Second).String()
		return m, updateElapsedTime()

	default:
		return m, nil
	}
}

func (m *upModel) View() string {
	if m.quitting {
		return "Operation canceled. Exiting...\n"
	}

	if m.err != nil {
		return fmt.Sprintf("Error: %s\n", m.err.Error())
	}

	if m.done {
		return fmt.Sprintf("✅ Adhar platform set up successfully!\nElapsed time: %s\n", m.elapsedTime)
	}

	status := m.status
	if status == "" {
		status = "Initializing..."
	}

	return fmt.Sprintf("%s\n%s\nElapsed time: %s\n", m.spinner.View(), status, m.elapsedTime)
}

func updateElapsedTime() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return logger.ElapsedTimeMsg(t)
	})
}

func ensureKindCluster() error {
	_, err := exec.LookPath("kind")
	if err != nil {
		logger.Logger.Error("kind command not found, please install kind")
		return errors.New("kind command not found, please install kind")
	}

	dockerCmd := exec.Command("docker", "info")
	if err := dockerCmd.Run(); err != nil {
		logger.Logger.Error("docker is not running, please start docker and try again")
		return errors.New("docker is not running, please start docker and try again")
	}

	cmd := exec.Command("kind", "get", "clusters")
	output, err := cmd.CombinedOutput()
	if err != nil {
		logger.Logger.Errorf("failed to check for existing kind clusters: %s", err)
		return fmt.Errorf("failed to check for existing kind clusters: %w", err)
	}

	if strings.Contains(string(output), kindClusterName) {
		logger.Logger.Info("Kind cluster already exists")
		return nil
	}

	logger.Logger.Info("Creating a new Kind cluster...")
	createCmd := exec.Command("kind", "create", "cluster", "--name", kindClusterName)
	if output, err := createCmd.CombinedOutput(); err != nil {
		logger.Logger.Errorf("failed to create kind cluster: %s", string(output))
		return fmt.Errorf("failed to create kind cluster: %w\n%s", err, string(output))
	}

	logger.Logger.Info("Kind cluster created successfully")
	return nil
}

func startManager() error {
	logger.Logger.Info("Starting the manager...")
	// Placeholder for actual manager start logic
	return nil
}

var upCmd = &cobra.Command{
	Use:   "up",
	Short: "Sets up the Adhar platform.",
	Run: func(cmd *cobra.Command, args []string) {
		if err := ensureKindCluster(); err != nil {
			fmt.Printf("Error ensuring Kind cluster: %s\n", err)
			os.Exit(1)
		}

		model := &upModel{
			spinner: spinner.New(),
		}
		p := tea.NewProgram(model)
		if _, err := p.Run(); err != nil {
			fmt.Println("Error running program:", err)
			os.Exit(1)
		}
	},
}

func init() {
	upCmd.Flags().BoolVarP(&waitForReadiness, "wait", "w", false, "Wait for all resources to be ready")
	upCmd.Flags().IntVarP(&timeout, "timeout", "t", 300, "Timeout in seconds for the operation")
	upCmd.Flags().StringVarP(&kubeconfigNamespace, "namespace", "n", "default", "Namespace to operate in")
	upCmd.Flags().BoolVar(&noSpinner, "no-spinner", false, "Disable spinner animation")
	upCmd.Flags().BoolVar(&verboseUp, "verbose", false, "Enable verbose output")
	upCmd.Flags().StringVarP(&environmentName, "file", "f", "adhar-config.yaml", "Path to the configuration file")

	// Add the upCmd to the root command
	rootCmd.AddCommand(upCmd)
}
