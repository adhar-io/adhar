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
	"fmt"
	"os"
	"strings"

	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/logger"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

// ASCII Art for ADHAR Platform
const adharArt = `
    _      ____    _    _      _      ____   
   / \    |  _ \  | |  | |    / \    |  _ \  
  / _ \   | | | | | |__| |   / _ \   | |_) | 
 / ___ \  | |_| | |  __  |  / ___ \  |  _ <  
/_/   \_\ |____/  |_|  |_| /_/   \_\ |_| \_\ `

// ANSI colors for terminal output
var (
	// Define some base colors
	primaryColor   = lipgloss.AdaptiveColor{Light: "#0366d6", Dark: "#58a6ff"}
	secondaryColor = lipgloss.AdaptiveColor{Light: "#28a745", Dark: "#3fb950"}
	accentColor    = lipgloss.AdaptiveColor{Light: "#6f42c1", Dark: "#8957e5"}
	errorColor     = lipgloss.AdaptiveColor{Light: "#cb2431", Dark: "#f85149"}
	warningColor   = lipgloss.AdaptiveColor{Light: "#f66a0a", Dark: "#f0883e"}
	infoColor      = lipgloss.AdaptiveColor{Light: "#0090ff", Dark: "#00b4ff"}
	highlightColor = lipgloss.AdaptiveColor{Light: "#e36209", Dark: "#ffab70"}

	// Define styles
	headerStyle = lipgloss.NewStyle().
			Foreground(primaryColor).
			Bold(true).
			MarginBottom(1)

	titleStyle = lipgloss.NewStyle().
			Foreground(accentColor).
			Bold(true).
			MarginLeft(1)

	subtitleStyle = lipgloss.NewStyle().
			Foreground(secondaryColor).
			Italic(true).
			MarginLeft(2)

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

	// Style for bullet points
	bulletStyle = lipgloss.NewStyle().
			Foreground(infoColor) // Or choose another suitable color

	// Style for command descriptions
	cmdDescStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#767676")).
			Italic(true)

	// Box styles for framing content
	borderStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(primaryColor).
			Padding(1).
			MarginTop(1).
			MarginBottom(1)

	// Focus style for selected items
	focusedStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(highlightColor).
			Padding(1).
			MarginTop(1).
			MarginBottom(1)

	// Table styles
	tableHeaderStyle = lipgloss.NewStyle().
				Foreground(primaryColor).
				Bold(true).
				Padding(0, 1)

	tableCellStyle = lipgloss.NewStyle().
			Padding(0, 1)

	tableRowStyle = lipgloss.NewStyle().
			Padding(0, 0, 0, 2)
)

// renderAsciiArt renders the ADHAR ASCII art with style
func renderAsciiArt() string {
	return lipgloss.NewStyle().
		Foreground(primaryColor).
		Bold(true).
		Render(adharArt)
}

// printHeader prints the standard Adhar Platform header with ASCII art
func printHeader() {
	fmt.Println(renderAsciiArt())
	fmt.Println(subtitleStyle.Render(" Platform " + globals.Version + " - The Open Foundation"))
	fmt.Println() // Add a blank line for spacing
}

// printFooter prints the standard Adhar Platform footer
func printFooter() {
	fmt.Println() // Add a blank line for spacing
	fmt.Println(lipgloss.NewStyle().Align(lipgloss.Center).Render(
		subtitleStyle.Render("Adhar • Built with ❤️ for developers!"),
	))
	fmt.Println() // Add a blank line for spacing
}

// renderCommandHeader prints a standardized header for any command with ASCII art and command-specific title
func renderCommandHeader(commandName, description string) {
	fmt.Println(renderAsciiArt())
	fmt.Println(subtitleStyle.Render(" Platform " + globals.Version + " - The Open Foundation"))
	if commandName != "" {
		fmt.Println(headerStyle.Render("ADHAR " + strings.ToUpper(commandName)))
		if description != "" {
			fmt.Println(subtitleStyle.Render(description))
		}
	}
	fmt.Println() // Add a blank line for spacing
}

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:     "adhar",
	Aliases: []string{"a", "ad"},
	Short:   "The Open Foundation for your Internal Developer Platform",
	Long: `Adhar streamlines your software development lifecycle with a comprehensive Internal Developer Platform built on Kubernetes and GitOps principles.

The platform provides unified tools for the complete development journey:
• Define & Plan: Structure projects and requirements
• Design & Build: Architect applications with templates and best practices  
• Deploy & Deliver: Ship confidently with GitOps to any environment
• Discover & Monitor: Gain insights with built-in observability
• Decide & Optimize: Make data-driven decisions for continuous improvement

Built for developer productivity with enterprise-grade security and governance.`,
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
func Execute() error {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "%s %v\n", errorStyle.Render("Error:"), err)
		os.Exit(1)
	}
	return nil
}

func init() {
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

// renderRootCommandContent renders the content for the root command
func renderRootCommandContent(cmd *cobra.Command) {
	// Print welcome message
	fmt.Println(infoStyle.Render("🚀 Welcome to Adhar Platform!"))
	fmt.Println()
	fmt.Println(infoStyle.Render("Adhar Platform is a comprehensive Internal Developer Platform (IDP) that provides"))
	fmt.Println(infoStyle.Render("unified Kubernetes-native approach for the entire software development lifecycle."))
	fmt.Println()

	// Platform Lifecycle
	fmt.Println(titleStyle.Render("📋 Platform Lifecycle"))
	fmt.Println("  " + bulletStyle.Render("•") + " " + cmdDescStyle.Render("up") + "       - Create and start the Adhar platform (local or cloud)")
	fmt.Println("  " + bulletStyle.Render("•") + " " + cmdDescStyle.Render("down") + "     - Stop and destroy the Adhar platform")
	fmt.Println("  " + bulletStyle.Render("•") + " " + cmdDescStyle.Render("status") + "   - Check platform health and status")
	fmt.Println()

	// Resource Management
	fmt.Println(titleStyle.Render("🔍 Resource Management"))
	fmt.Println("  " + bulletStyle.Render("•") + " " + cmdDescStyle.Render("get") + "       - Display platform resources (apps, secrets, clusters, etc.)")
	fmt.Println("  " + bulletStyle.Render("•") + " " + cmdDescStyle.Render("apps") + "     - Manage application lifecycle and deployments")
	fmt.Println("  " + bulletStyle.Render("•") + " " + cmdDescStyle.Render("cluster") + "  - Manage Kubernetes clusters and configurations")
	fmt.Println("  " + bulletStyle.Render("•") + " " + cmdDescStyle.Render("config") + "   - Manage platform configuration and settings")
	fmt.Println("  " + bulletStyle.Render("•") + " " + cmdDescStyle.Render("env") + "      - Manage platform environments (dev, staging, prod)")
	fmt.Println()

	// Operations & Monitoring
	fmt.Println(titleStyle.Render("⚡ Operations & Monitoring"))
	fmt.Println("  " + bulletStyle.Render("•") + " " + cmdDescStyle.Render("health") + "   - Check platform health, services, and dependencies")
	fmt.Println("  " + bulletStyle.Render("•") + " " + cmdDescStyle.Render("logs") + "     - View centralized platform logs and events")
	fmt.Println("  " + bulletStyle.Render("•") + " " + cmdDescStyle.Render("metrics") + "  - Access platform metrics and performance data")
	fmt.Println("  " + bulletStyle.Render("•") + " " + cmdDescStyle.Render("traces") + "   - View distributed tracing information")
	fmt.Println()

	// Security & Compliance
	fmt.Println(titleStyle.Render("🔒 Security & Compliance"))
	fmt.Println("  " + bulletStyle.Render("•") + " " + cmdDescStyle.Render("security") + " - Security scanning, vulnerability management, and policies")
	fmt.Println("  " + bulletStyle.Render("•") + " " + cmdDescStyle.Render("auth") + "     - Authentication, authorization, and user management")
	fmt.Println("  " + bulletStyle.Render("•") + " " + cmdDescStyle.Render("secrets") + "  - Manage secrets, certificates, and sensitive data")
	fmt.Println("  " + bulletStyle.Render("•") + " " + cmdDescStyle.Render("policy") + "   - Platform policy management and governance")
	fmt.Println()

	// GitOps & DevOps
	fmt.Println(titleStyle.Render("🔄 GitOps & DevOps"))
	fmt.Println("  " + bulletStyle.Render("•") + " " + cmdDescStyle.Render("gitops") + "  - GitOps operations, ArgoCD management, and workflows")
	fmt.Println("  " + bulletStyle.Render("•") + " " + cmdDescStyle.Render("pipeline") + " - CI/CD pipeline creation, execution, and management")
	fmt.Println("  " + bulletStyle.Render("•") + " " + cmdDescStyle.Render("webhook") + " - Webhook management and integration endpoints")
	fmt.Println()

	// Infrastructure & Data
	fmt.Println(titleStyle.Render("🏗️ Infrastructure & Data"))
	fmt.Println("  " + bulletStyle.Render("•") + " " + cmdDescStyle.Render("network") + " - Network diagnostics, policies, and connectivity testing")
	fmt.Println("  " + bulletStyle.Render("•") + " " + cmdDescStyle.Render("db") + "      - Database management, operations, and monitoring")
	fmt.Println("  " + bulletStyle.Render("•") + " " + cmdDescStyle.Render("storage") + " - Storage management, volumes, and data persistence")
	fmt.Println("  " + bulletStyle.Render("•") + " " + cmdDescStyle.Render("service") + " - Service management, load balancing, and API endpoints")
	fmt.Println()

	// Resource Management & Optimization
	fmt.Println(titleStyle.Render("📊 Resource Management & Optimization"))
	fmt.Println("  " + bulletStyle.Render("•") + " " + cmdDescStyle.Render("scale") + "   - Resource scaling, auto-scaling, and optimization")
	fmt.Println("  " + bulletStyle.Render("•") + " " + cmdDescStyle.Render("backup") + "  - Platform backup creation and management")
	fmt.Println("  " + bulletStyle.Render("•") + " " + cmdDescStyle.Render("restore") + " - Platform restoration from backups")
	fmt.Println()

	// Utilities
	fmt.Println(titleStyle.Render("🛠️ Utilities"))
	fmt.Println("  " + bulletStyle.Render("•") + " " + cmdDescStyle.Render("help") + "     - Get help about commands (this command)")
	fmt.Println("  " + bulletStyle.Render("•") + " " + cmdDescStyle.Render("version") + "  - Show version information and build details")
	fmt.Println()

	// Quick Start Examples
	fmt.Println(titleStyle.Render("🚀 Quick Start Examples"))
	fmt.Println("  " + bulletStyle.Render("•") + " " + cmdDescStyle.Render("adhar up") + "                    - Create local development platform")
	fmt.Println("  " + bulletStyle.Render("•") + " " + cmdDescStyle.Render("adhar up -f config.yaml") + "   - Deploy production platform")
	fmt.Println("  " + bulletStyle.Render("•") + " " + cmdDescStyle.Render("adhar get status") + "          - Check platform health")
	fmt.Println("  " + bulletStyle.Render("•") + " " + cmdDescStyle.Render("adhar get secrets") + "         - View platform credentials")
	fmt.Println("  " + bulletStyle.Render("•") + " " + cmdDescStyle.Render("adhar down") + "                - Clean up local platform")
	fmt.Println()

	// Help Information
	fmt.Println(titleStyle.Render("📚 Getting More Help"))
	fmt.Println("  " + bulletStyle.Render("•") + " " + infoStyle.Render("Command-specific help:") + " " + highlightStyle.Render("adhar help <command>"))
	fmt.Println("  " + bulletStyle.Render("•") + " " + infoStyle.Render("Detailed documentation:") + " " + highlightStyle.Render("https://docs.adhar.io"))
	fmt.Println("  " + bulletStyle.Render("•") + " " + infoStyle.Render("Community support:") + " " + highlightStyle.Render("https://github.com/adhar-io/adhar"))
	fmt.Println()

	// Tips
	fmt.Println(titleStyle.Render("💡 Pro Tips"))
	fmt.Println("  " + bulletStyle.Render("•") + " Use " + highlightStyle.Render("--dry-run") + " flag to preview changes without applying them")
	fmt.Println("  " + bulletStyle.Render("•") + " Use " + highlightStyle.Render("--verbose") + " flag for detailed output and debugging")
	fmt.Println("  " + bulletStyle.Render("•") + " Use " + highlightStyle.Render("--no-color") + " flag to disable colored output")
	fmt.Println("  " + bulletStyle.Render("•") + " Use " + highlightStyle.Render("--kubeconfig") + " to specify custom kubeconfig path")
	fmt.Println()
}
