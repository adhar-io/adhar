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
	"adhar-io/adhar/platform/config"
	"adhar-io/adhar/platform/logger"
	"adhar-io/adhar/platform/utils"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// DownCmd represents the down command
var DownCmd = &cobra.Command{
	Use:   "down",
	Short: "Tears down an Adhar environment and cleans up its resources",
	Long: `The 'down' command removes an Adhar environment.

With no flags it tears down the local Kind cluster named '` + globals.DefaultClusterName + `'.

Given a configuration file it tears down the CLOUD cluster that 'adhar up'
created for that environment -- droplets/instances, volumes, load balancers,
firewall, VPC and SSH key -- through the same provider that created them.

Pass --file (and --env) whenever the environment is not local. Without them
this command only ever looks at Kind, which on a cloud environment deletes
nothing while still reporting success.

During execution:
- Press 'i' to toggle detailed output
- Press Ctrl+C to cancel the operation

Examples:
  # Tear down the local Kind environment
  adhar down

  # Tear down one cloud environment
  adhar down -f config.yaml --env dev

  # Tear down every environment in the file
  adhar down -f config.yaml

  # Skip the confirmation prompt
  adhar down -f config.yaml --env dev --force

  # Also remove unattached pvc-* volumes left in the region
  adhar down -f config.yaml --env dev --purge-orphaned-volumes

  # Show detailed information during tear down
  adhar down --verbose`,
	Run: func(cmd *cobra.Command, args []string) {
		// Confirm BEFORE the TUI takes over the terminal: a cloud teardown
		// destroys machines, volumes and load balancers that cost money and
		// cannot be undone, and a full-screen spinner is no place to ask.
		if downConfigFile != "" && !forceDelete {
			target := downEnv
			if target == "" {
				target = "EVERY environment in " + downConfigFile
			}
			fmt.Printf("\nThis permanently deletes the cloud resources for %s:\n", target)
			fmt.Printf("  instances, block volumes, load balancers, firewall, VPC and SSH key.\n")
			fmt.Printf("Type 'yes' to confirm: ")
			var confirmation string
			fmt.Scanln(&confirmation)
			if confirmation != "yes" {
				fmt.Println("Teardown cancelled.")
				return
			}
		}

		// Without a terminal the Bubble Tea program cannot start at all
		// ("could not open a new TTY"), which made `adhar down` unusable from
		// CI, a script, or any non-interactive shell -- while `adhar up` works
		// fine there. Fall back to plain streamed output instead.
		if !stdoutIsTerminal() {
			runTeardownPlain()
			return
		}

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
	forceDelete    bool
	verboseDown    bool
	noAnimation    bool
	downConfigFile string
	downEnv        string
	purgeVolumes   bool
)

func init() {
	// NOTE: -f is --file here, matching `adhar up` and `adhar cluster delete`.
	// It used to be the shorthand for --force, which meant `adhar down -f
	// config.yaml` force-deleted the local Kind cluster and ignored the file
	// entirely -- the exact command someone reaches for to tear down a cloud
	// environment. --force keeps its long form only.
	DownCmd.Flags().StringVarP(&downConfigFile, "file", "f", "", "Configuration file describing the environment to tear down")
	DownCmd.Flags().StringVar(&downEnv, "env", "", "Environment to tear down (defaults to every environment in --file)")
	DownCmd.Flags().BoolVar(&forceDelete, "force", false, "Skip the confirmation prompt")
	DownCmd.Flags().BoolVar(&purgeVolumes, "purge-orphaned-volumes", false,
		"Also delete unattached pvc-* block-storage volumes in the cluster's region that carry no other cluster's tag. "+
			"Only safe when no other Kubernetes cluster uses that region.")
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
		// Same width and shape as every other frame. A bare multi-line string
		// here is drawn over the previous bordered frame instead of replacing
		// it, which is what produced the torn box mid-sentence.
		body := fmt.Sprintf("%s %s\n\n%s %s",
			helpers.ErrorStyle.Render("✗"),
			helpers.ErrorStyle.Render("Failed to tear down the environment"),
			helpers.InfoStyle.Render("Reason:"),
			wrapText(m.err.Error(), boxTextWidth))

		if hint := teardownHint(m.err); hint != "" {
			body += "\n\n" + helpers.InfoStyle.Render(wrapText(hint, boxTextWidth))
		}

		return fmt.Sprintf("\n%s\n", helpers.BorderStyle.Width(boxWidth).Render(body))
	}

	if m.done {
		// Name what was actually removed: saying "Kind cluster" after a cloud
		// teardown is the kind of wrong detail that makes someone re-check
		// their account to be sure anything happened.
		removed := "Kind cluster and resources have been removed"
		if downConfigFile != "" {
			target := downEnv
			if target == "" {
				target = "every environment in " + downConfigFile
			}
			removed = "Cloud resources for " + target + " have been removed"
		}
		successBox := helpers.BorderStyle.Width(boxWidth).Render(
			fmt.Sprintf("%s %s\n\n%s\n",
				helpers.SuccessStyle.Render("✓"),
				helpers.SuccessStyle.Render("Successfully tore down Adhar platform!"),
				helpers.SubtitleStyle.Render(wrapText(removed, boxTextWidth))))

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
	mainContent := helpers.BorderStyle.Width(boxWidth).Render(
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

	// A configuration file means the environment may live in a cloud, and the
	// Kind path below would silently do nothing there.
	if downConfigFile != "" {
		teardownFromConfig(emit, detail)
		return
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
		detail("✗ No Kind cluster named '%s' found", globals.DefaultClusterName)
		// Keep this to ONE line: the View renders it inside a fixed-width box,
		// and embedded newlines paint over the frame beneath. The guidance for
		// cloud environments is added by the View, styled, on its own lines.
		emit(logger.ErrorMsg{Err: fmt.Errorf("no local Kind cluster named %q exists", globals.DefaultClusterName)})
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
		// Fallback: force-remove the node containers for this cluster.
		// Uses whichever engine hosts the cluster -- Podman and the nerdctl
		// family accept the same subcommands, and hardcoding `docker` here made
		// teardown a no-op on a Podman-only machine.
		eng := utils.DetectContainerEngine()
		detail("! kind delete failed for '%s' (%v) — removing containers directly with %s", clusterName, err, eng.Binary)
		for _, suffix := range []string{"-control-plane", "-worker", "-worker2"} {
			_ = exec.Command(eng.Binary, "rm", "-f", clusterName+suffix).Run()
		}
		detail("  removed any leftover '%s' %s containers", clusterName, eng.Name)
	}

	// Step 3: Clean up the kind network
	eng := utils.DetectContainerEngine()
	emit(logger.StepMsg("Cleaning up"))
	emit(logger.StatusMsg(fmt.Sprintf("Removing the 'kind' %s network...", eng.Name)))
	detail("→ %s network rm kind", eng.Binary)
	_ = exec.Command(eng.Binary, "network", "rm", "kind").Run()

	if !deleted {
		// Check if containers were at least removed via the fallback
		out, _ := exec.Command(eng.Binary, "ps", "-a", "--filter", "name=adhar", "--format", "{{.Names}}").CombinedOutput()
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

// kindClusterExists checks if the Kind cluster exists and verifies that a
// container engine is running.
//
// Kind runs its nodes on Docker, Podman or a nerdctl-family engine, and which
// one is in use is decided by KIND_EXPERIMENTAL_PROVIDER or by what is
// installed. Demanding Docker specifically made this fail on a working
// Podman machine before it ever looked for the cluster.
func kindClusterExists() (bool, error) {
	eng := utils.DetectContainerEngine()
	if !eng.Available {
		return false, fmt.Errorf(
			"no container engine is running: tried %s. Start one (or set KIND_EXPERIMENTAL_PROVIDER) before tearing down a local cluster",
			strings.Join(utils.EngineNames(), ", "))
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

// teardownFromConfig removes the cluster(s) an environment file describes,
// through the provider that created them.
//
// This is the path `adhar down` never had. It only ever ran
// `kind delete cluster`, so on a DigitalOcean environment it destroyed nothing,
// printed a success banner, and left the droplets, block volumes, load balancer,
// firewall and VPC running and billing.
func teardownFromConfig(emit func(tea.Msg), detail func(string, ...interface{})) {
	emit(logger.StepMsg("Reading configuration"))
	emit(logger.StatusMsg(fmt.Sprintf("Loading %s...", downConfigFile)))

	cfg, err := config.LoadConfig(downConfigFile)
	if err != nil {
		detail("✗ %v", err)
		emit(logger.ErrorMsg{Err: fmt.Errorf("loading %s: %w", downConfigFile, err)})
		return
	}
	// LoadConfig parses the file; it does NOT expand templates into
	// ResolvedEnvironments. Skipping this made every environment look absent
	// ("environment \"dev\" is not defined") on a file that plainly defines it.
	if err := cfg.ResolveEnvironments(); err != nil {
		detail("✗ %v", err)
		emit(logger.ErrorMsg{Err: fmt.Errorf("resolving environments in %s: %w", downConfigFile, err)})
		return
	}

	// Which environments to remove: the named one, or all of them.
	var envNames []string
	switch {
	case downEnv != "":
		if _, ok := cfg.ResolvedEnvironments[downEnv]; !ok {
			emit(logger.ErrorMsg{Err: fmt.Errorf("environment %q is not defined in %s", downEnv, downConfigFile)})
			return
		}
		envNames = []string{downEnv}
	default:
		for name := range cfg.ResolvedEnvironments {
			envNames = append(envNames, name)
		}
		sort.Strings(envNames)
		if len(envNames) == 0 {
			emit(logger.ErrorMsg{Err: fmt.Errorf("no environments defined in %s", downConfigFile)})
			return
		}
		detail("→ No --env given; tearing down every environment in the file: %s", strings.Join(envNames, ", "))
	}

	providerOpts := map[string]interface{}{}
	if purgeVolumes {
		providerOpts["purgeOrphanedVolumes"] = true
	}

	ctx := context.Background()
	var failures []string
	for _, envName := range envNames {
		env := cfg.ResolvedEnvironments[envName]
		clusterName := helpers.EnvironmentClusterName(env)

		emit(logger.StepMsg(fmt.Sprintf("Tearing down %s", envName)))
		emit(logger.StatusMsg(fmt.Sprintf("Locating cluster '%s'...", clusterName)))
		detail("→ environment %s: provider=%s cluster=%s", envName, env.ResolvedProvider, clusterName)

		found, err := helpers.FindCluster(ctx, cfg, clusterName, providerOpts, func(w string) { detail("  ! %s", w) })
		if err != nil {
			// Already gone is a success for a teardown, not a failure.
			detail("  %v", err)
			emit(logger.StatusMsg(fmt.Sprintf("Nothing to remove for '%s'", envName)))
			continue
		}

		detail("  found in provider %s (id %s, status %s)", found.ProviderName, found.Cluster.ID, found.Cluster.Status)
		if !found.IsAdharManaged() {
			detail("  ! cluster is not tagged adhar.io/managed-by=adhar; deleting anyway")
		}

		emit(logger.StatusMsg(fmt.Sprintf("Deleting cluster '%s' (%s)...", clusterName, found.ProviderName)))
		if err := found.Provider.DeleteCluster(ctx, found.Cluster.ID); err != nil {
			detail("  ✗ %v", err)
			failures = append(failures, fmt.Sprintf("%s: %v", envName, err))
			continue
		}
		detail("  ✓ deleted %s", clusterName)
	}

	emit(logger.StepMsg("Cleaning up"))
	emit(logger.StatusMsg("Removing leftover kubeconfig files..."))
	removed := cleanupFiles()
	if len(removed) == 0 {
		detail("→ No leftover kubeconfig files to remove")
	} else {
		for _, f := range removed {
			detail("  removed %s", f)
		}
	}

	if len(failures) > 0 {
		emit(logger.ErrorMsg{Err: fmt.Errorf("teardown failed for: %s", strings.Join(failures, "; "))})
		return
	}

	emit(logger.StatusMsg("Teardown complete"))
	detail("✓ Teardown complete")
	detail("  Verify with: adhar cluster list --file %s", downConfigFile)
	emit(logger.DoneMsg{})
}

// Box geometry. Every frame this command renders uses the same width, so the
// final frame fully replaces the one before it instead of painting over part
// of it.
const (
	boxWidth     = 60
	boxTextWidth = boxWidth - 4 // the border and its padding
)

// teardownHint turns a failure into the next thing to try. It is kept out of
// the error text itself so the error stays one line.
func teardownHint(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "no local Kind cluster"):
		return "If this environment runs in a cloud, pass its configuration file: " +
			"adhar down -f <config.yaml> --env <environment>"
	case strings.Contains(msg, "not found in any configured provider"):
		return "Check the environment name and that the configuration file lists the right provider: " +
			"adhar cluster list -f <config.yaml>"
	case strings.Contains(msg, "permission"), strings.Contains(msg, "access"), strings.Contains(msg, "401"):
		return "The provider rejected the credentials. Check the API token in the environment and its scopes."
	default:
		return "Re-run with --verbose, or press 'i' during teardown, to see the provider output."
	}
}

// wrapText breaks s onto lines of at most width runes, on word boundaries, so
// long messages stay inside the box border.
func wrapText(s string, width int) string {
	if width <= 0 {
		return s
	}
	var lines []string
	for _, paragraph := range strings.Split(s, "\n") {
		line := ""
		for _, word := range strings.Fields(paragraph) {
			switch {
			case line == "":
				line = word
			case len(line)+1+len(word) <= width:
				line += " " + word
			default:
				lines = append(lines, line)
				line = word
			}
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// stdoutIsTerminal reports whether the spinner UI can be driven at all.
func stdoutIsTerminal() bool {
	return term.IsTerminal(int(os.Stdout.Fd()))
}

// runTeardownPlain performs the same teardown without the full-screen UI,
// printing each step as a line. This is what CI, scripts and piped output get.
func runTeardownPlain() {
	sub := make(chan tea.Msg)
	go teardown(sub)

	for msg := range sub {
		switch m := msg.(type) {
		case logger.StepMsg:
			fmt.Printf("\n==> %s\n", string(m))
		case logger.StatusMsg:
			fmt.Printf("    %s\n", string(m))
		case logger.ExtraOutputMsg:
			// Detail lines are the 'i' pane in the UI; --verbose asks for them.
			if verboseDown {
				for _, line := range strings.Split(string(m), "\n") {
					if strings.TrimSpace(line) != "" {
						fmt.Printf("      %s\n", line)
					}
				}
			}
		case logger.ErrorMsg:
			fmt.Fprintf(os.Stderr, "\nError: %v\n", m.Err)
			if hint := teardownHint(m.Err); hint != "" {
				fmt.Fprintf(os.Stderr, "%s\n", hint)
			}
			os.Exit(1)
		case logger.DoneMsg:
			fmt.Printf("\nTeardown complete.\n")
			return
		}
	}
}
