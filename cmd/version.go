package main

import (
	"fmt"
	"os/exec"
	"regexp"
	"runtime"
	"strings"

	"adhar-io/adhar/globals"

	"github.com/spf13/cobra"
)

// Version information is now provided through globals package

func init() {
	// Add the version command to the root command
	AddCommand(versionCmd)
}

// versionCmd represents the version command
var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version information of Adhar",
	Long:  `Display detailed version information about the Adhar Platform, including version number, git commit, and build date.`,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		// Skip the default PersistentPreRun to avoid duplicate ASCII art
	},
	Run: func(cmd *cobra.Command, args []string) {
		// Print the header with ASCII art and version
		printHeader()

		// Create a pretty box for version info
		versionInfo := fmt.Sprintf(
			"%s %s\n%s %s\n%s %s\n%s %s\n%s %s",
			titleStyle.Render("Version:"), highlightStyle.Render(globals.Version),
			titleStyle.Render("Git Commit:"), globals.GitCommit,
			titleStyle.Render("Build Date:"), globals.BuildDate,
			titleStyle.Render("Go Version:"), highlightStyle.Render(runtime.Version()),
			titleStyle.Render("OS/Arch:"), fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
		)

		// Display the version information in a box with consistent formatting
		versionBox := borderStyle.Width(50).Padding(1, 2).Render(versionInfo)
		fmt.Println(versionBox)

		// Show additional system information for troubleshooting
		fmt.Println()
		fmt.Println(subtitleStyle.Render("System Information:"))

		// Check if required dependencies are available
		checkDependencies()
	},
}

// checkDependencies verifies that required tools are installed
func checkDependencies() {
	dependencies := []struct {
		name    string
		command string
		args    []string
	}{
		{"Docker", "docker", []string{"--version"}},
		{"Kind", "kind", []string{"--version"}},
		{"kubectl", "kubectl", []string{"version", "--client", "--output=yaml"}},
		{"Helm", "helm", []string{"version", "--short"}},
	}

	for _, dep := range dependencies {
		status := "✓ Available"
		info := ""

		cmd := exec.Command(dep.command, dep.args...)
		output, err := cmd.CombinedOutput()

		if err != nil {
			status = "✗ Not found"
			info = "Required for platform functionality"
		} else {
			// Special handling for kubectl YAML output
			if dep.command == "kubectl" && strings.Contains(string(dep.args[len(dep.args)-1]), "yaml") {
				// Extract version from YAML output using regex
				versionRegex := regexp.MustCompile(`gitVersion:\s*"?([^"\s]+)"?`)
				if match := versionRegex.FindStringSubmatch(string(output)); len(match) > 1 {
					info = fmt.Sprintf("Client Version: %s", match[1])
				} else {
					info = "Version info available"
				}
			} else {
				// Extract just the first line of output for cleaner display
				lines := strings.Split(strings.TrimSpace(string(output)), "\n")
				if len(lines) > 0 {
					info = strings.TrimSpace(lines[0])
				}
			}
		}

		fmt.Printf("  %s: %s %s\n",
			titleStyle.Render(dep.name),
			status,
			infoStyle.Render(info))
	}
}
