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

package version

import (
	"fmt"
	"os/exec"
	"regexp"
	"runtime"
	"strings"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
)

// Version information - these are set at build time via ldflags
var (
	Version   string = "0.0.1-dev" // Set at build time
	BuildDate string = "unknown"   // Set at build time
	GitCommit string = "unknown"   // Set at build time

	// Version command flags
	short bool
)

func init() {
	VersionCmd.Flags().BoolVarP(&short, "short", "s", false, "Show only version number")
}

// VersionCmd represents the version command
var VersionCmd = &cobra.Command{
	Use:     "version",
	Aliases: []string{"v", "ver"},
	Short:   "Print the version information of Adhar",
	Long:    `Display detailed version information about the Adhar Platform, including version number, git commit, and build date.`,

	Run: func(cmd *cobra.Command, args []string) {

		// Handle short version output
		if short {
			fmt.Println(Version)
			return
		}

		// Create a pretty box for version info
		versionInfo := fmt.Sprintf(
			"%s %s\n%s %s\n%s %s\n%s %s\n%s %s",
			helpers.TitleStyle.Render("Version:"), helpers.HighlightStyle.Render(Version),
			helpers.TitleStyle.Render("Git Commit:"), GitCommit,
			helpers.TitleStyle.Render("Build Date:"), BuildDate,
			helpers.TitleStyle.Render("Go Version:"), helpers.HighlightStyle.Render(runtime.Version()),
			helpers.TitleStyle.Render("OS/Arch:"), fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
		)

		// Display the version information in a box with consistent formatting
		versionBox := helpers.CreateBox(versionInfo, 50)
		fmt.Println(versionBox)

		// Show additional system information for troubleshooting
		fmt.Println()
		fmt.Println(helpers.SubtitleStyle.Render("System Information:"))

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
		{"Kubectl", "kubectl", []string{"version", "--client", "--output=yaml"}},
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
			helpers.TitleStyle.Render(dep.name),
			status,
			helpers.InfoStyle.Render(info))
	}
}
