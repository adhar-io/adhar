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

package main

import (
	"context"
	"fmt"
	"os"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/logger"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

// printHeader prints the signature Adhar header: the hexagon brand mark rendered
// as truecolor terminal art alongside the gradient wordmark and tagline.
// See cmd/helpers/branding.go — the mark shares its geometry with the SVG logo.
// When stdout is not an interactive terminal (piped, redirected, CI) it falls
// back to a clean one-line brand strip so logs stay tidy.
func printHeader() {
	if fi, err := os.Stdout.Stat(); err != nil || (fi.Mode()&os.ModeCharDevice) == 0 {
		fmt.Println(helpers.RenderBannerLine(globals.Version))
		return
	}
	fmt.Println()
	fmt.Println(helpers.RenderBanner())
	fmt.Println() // Add a blank line for spacing
}

// printFooter prints the standard Adhar Platform footer
func printFooter() {
	fmt.Println()
	fmt.Println(lipgloss.NewStyle().Align(lipgloss.Center).Render(
		helpers.SubtitleStyle.Render("Adhar • Built with ❤️ for developers!"),
	))
	fmt.Println()
}

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:     "adhar",
	Aliases: []string{"a", "ad"},
	Short:   "The Open Foundation for your Internal Developer Platform",
	Long: `Adhar streamlines your software development lifecycle with a comprehensive Internal Developer Platform built on Kubernetes and GitOps principles.

The platform provides unified tools for the complete development journey:
• Define: Structure projects and requirements with declarative configurations
• Design: Architect applications using proven templates and best practices
• Develop: Build and test applications in isolated, reproducible environments
• Deliver: Deploy confidently with GitOps automation to any environment
• Discover: Monitor and gain insights with comprehensive observability
• Decide: Make data-driven decisions using metrics and analytics

Getting Started:
  adhar up                 Spin up a complete local platform (Kind)
  adhar get status         Check that everything is healthy
  adhar get secrets        Grab your ArgoCD & Gitea credentials
  adhar down               Tear it all down when you're done

Built for developer productivity with enterprise-grade security and governance.`,
	Example: `  # Launch a complete local platform in minutes
  adhar up

  # Preview what 'up' would do, without changing anything
  adhar up --dry-run

  # Check platform health and grab credentials
  adhar get status
  adhar get secrets -p argocd

  # Tear the local platform down
  adhar down`,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		// Print header before any command runs
		// Skip header for help command itself to avoid duplication with Cobra's default help flag behavior
		if cmd.Name() != "help" && cmd.Name() != "__complete" && cmd.Name() != "__completeNoDesc" { // Avoid printing for built-in completion commands too
			// Check if the --help flag was used
			helpFlag, _ := cmd.Flags().GetBool("help")
			if !helpFlag {
				// Check if header should be hidden
				noHeader, _ := cmd.Flags().GetBool("no-header")

				// Special case: hide header for version command with --short flag
				if cmd.Name() == "version" {
					shortFlag, _ := cmd.Flags().GetBool("short")
					if shortFlag {
						noHeader = true
					}
				}

				if !noHeader {
					printHeader()
				}
			}
		}
	},
	PersistentPostRun: func(cmd *cobra.Command, args []string) {
		// Print footer after any command runs
		// Skip footer for help command itself to avoid duplication
		if cmd.Name() != "help" && cmd.Name() != "__complete" && cmd.Name() != "__completeNoDesc" {
			// Check if footer should be hidden
			noFooter, _ := cmd.Flags().GetBool("no-footer")

			// Special case: hide footer for version command with --short flag
			if cmd.Name() == "version" {
				shortFlag, _ := cmd.Flags().GetBool("short")
				if shortFlag {
					noFooter = true
				}
			}

			if !noFooter {
				printFooter()
			}
		}
	},
	Run: func(cmd *cobra.Command, args []string) {
		// Use the shared content rendering function
		renderRootCommandContent(cmd)
	},
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute(ctx context.Context) error {
	if err := rootCmd.ExecuteContext(ctx); err != nil {
		// FriendlyError has already rendered a styled message + hint, so avoid
		// printing the raw error a second time.
		if !helpers.IsFriendlyError(err) {
			fmt.Fprintf(os.Stderr, "%s %v\n", helpers.ErrorStyle.Render("Error:"), err)
		}
		os.Exit(1)
	}
	return nil
}

func init() {
	// Use the polished, persona-grouped help layout for the whole command tree
	// (`adhar --help`, `adhar <cmd> --help`, and `adhar help <cmd>`).
	rootCmd.SetHelpFunc(styledHelp)

	// Here you will define your flags and configuration settings.
	// Cobra supports persistent flags, which, if defined here,
	// Add global flags that apply to all commands
	rootCmd.PersistentFlags().BoolP("verbose", "v", false, "Enable verbose output")
	rootCmd.PersistentFlags().Bool("no-color", false, "Disable color output")
	rootCmd.PersistentFlags().Bool("debug", false, "Enable debug mode")
	rootCmd.PersistentFlags().String("theme", "auto", "Theme for markdown rendering (auto, dark, light)")
	rootCmd.PersistentFlags().String("kubeconfig", "", "Path to the kubeconfig file to use for Platform requests")
	rootCmd.PersistentFlags().Bool("no-header", false, "Hide the Adhar Platform header")
	rootCmd.PersistentFlags().Bool("no-footer", false, "Hide the Adhar Platform footer")

	// Added log level and colored output flags
	rootCmd.PersistentFlags().StringVar(&logger.CLILogLevel, "log-level", "info", logger.LogLevelMsg)
	rootCmd.PersistentFlags().BoolVar(&logger.CLIColoredOutput, "colored-logs", true, logger.ColoredOutputMsg)
}

// AddCommand adds one or more commands to the root command
func AddCommand(cmd ...*cobra.Command) {
	rootCmd.AddCommand(cmd...)
}

// Command group IDs organize subcommands in `adhar --help` around the people who
// use them: Developers ship apps and self-serve resources; Observers watch health
// and telemetry; Operators run day-2 operations; Administrators own the platform
// lifecycle and governance. The same binary serves all three — what each person
// can actually do is gated by their Keycloak group → Kubernetes RBAC.
const (
	GroupDevelop    = "develop"
	GroupObserve    = "observe"
	GroupOperate    = "operate"
	GroupAdminister = "administer"
	GroupUtilities  = "utilities"

	// Back-compat aliases (kept so any external references still compile).
	GroupPlatform      = GroupAdminister
	GroupCluster       = GroupOperate
	GroupApps          = GroupDevelop
	GroupObservability = GroupObserve
	GroupSecurity      = GroupAdminister
)

// RegisterCommandGroups registers the help groups so related commands appear
// together under friendly, persona-oriented headings in `adhar --help`.
func RegisterCommandGroups() {
	rootCmd.AddGroup(
		&cobra.Group{ID: GroupDevelop, Title: "Develop — build, ship & self-serve resources:"},
		&cobra.Group{ID: GroupObserve, Title: "Observe — health, logs, metrics & traces:"},
		&cobra.Group{ID: GroupOperate, Title: "Operate — day-2 operations:"},
		&cobra.Group{ID: GroupAdminister, Title: "Administer — platform lifecycle & governance:"},
		&cobra.Group{ID: GroupUtilities, Title: "Utilities — tooling:"},
	)
}

// renderRootCommandContent renders the root help layout for the bare `adhar`
// invocation. The banner/footer are printed by PersistentPreRun/PostRun, so the
// shared renderer is invoked without its own banner.
func renderRootCommandContent(cmd *cobra.Command) {
	renderHelp(cmd, nil, false)
}
