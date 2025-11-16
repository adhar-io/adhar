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

	"adhar-io/adhar/cmd/helpers"
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

// renderAsciiArt renders the ADHAR ASCII art with style
func renderAsciiArt() string {
	return lipgloss.NewStyle().
		Foreground(helpers.PrimaryColor).
		Bold(true).
		Render(adharArt)
}

// printHeader prints the standard Adhar Platform header with ASCII art
func printHeader() {
	fmt.Println(renderAsciiArt())
	fmt.Println(helpers.SubtitleStyle.Render(" Platform " + globals.Version + " - The Open Foundation"))
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
func Execute(ctx context.Context) error {
	return rootCmd.ExecuteContext(ctx)
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
func renderRootCommandContent(_ *cobra.Command) {
	// Print welcome message
	fmt.Println(helpers.InfoStyle.Render("🚀 Welcome to Adhar Platform!"))
	fmt.Println()
	fmt.Println(helpers.InfoStyle.Render("Adhar Platform is a comprehensive Internal Developer Platform (IDP) that provides"))
	fmt.Println(helpers.InfoStyle.Render("unified Kubernetes-native approach for the entire software development lifecycle."))
	fmt.Println()

	// Platform Lifecycle
	fmt.Println(helpers.TitleStyle.Render("📋 Platform Lifecycle"))
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " " + helpers.CmdDescStyle.Render("up") + "       - Create and start the Adhar platform (local or cloud)")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " " + helpers.CmdDescStyle.Render("down") + "     - Stop and destroy the Adhar platform")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " " + helpers.CmdDescStyle.Render("status") + "   - Check platform health and status")
	fmt.Println()

	// Resource Management
	fmt.Println(helpers.TitleStyle.Render("🔍 Resource Management"))
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " " + helpers.CmdDescStyle.Render("get") + "      - Display platform resources (apps, secrets, clusters, etc.)")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " " + helpers.CmdDescStyle.Render("apps") + "     - Manage application lifecycle and deployments")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " " + helpers.CmdDescStyle.Render("cluster") + "  - Manage Kubernetes clusters and configurations")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " " + helpers.CmdDescStyle.Render("config") + "   - Manage platform configuration and settings")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " " + helpers.CmdDescStyle.Render("env") + "      - Manage platform environments (dev, staging, prod)")
	fmt.Println()

	// Operations & Monitoring
	fmt.Println(helpers.TitleStyle.Render("⚡ Operations & Monitoring"))
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " " + helpers.CmdDescStyle.Render("health") + "   - Check platform health, services, and dependencies")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " " + helpers.CmdDescStyle.Render("logs") + "     - View centralized platform logs and events")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " " + helpers.CmdDescStyle.Render("metrics") + "  - Access platform metrics and performance data")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " " + helpers.CmdDescStyle.Render("traces") + "   - View distributed tracing information")
	fmt.Println()

	// Security & Compliance
	fmt.Println(helpers.TitleStyle.Render("🔒 Security & Compliance"))
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " " + helpers.CmdDescStyle.Render("security") + " - Security scanning, vulnerability management, and policies")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " " + helpers.CmdDescStyle.Render("auth") + "     - Authentication, authorization, and user management")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " " + helpers.CmdDescStyle.Render("secrets") + "  - Manage secrets, certificates, and sensitive data")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " " + helpers.CmdDescStyle.Render("policy") + "   - Platform policy management and governance")
	fmt.Println()

	// GitOps & DevOps
	fmt.Println(helpers.TitleStyle.Render("🔄 GitOps & DevOps"))
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " " + helpers.CmdDescStyle.Render("gitops") + "   - GitOps operations, ArgoCD management, and workflows")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " " + helpers.CmdDescStyle.Render("pipeline") + " - CI/CD pipeline creation, execution, and management")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " " + helpers.CmdDescStyle.Render("webhook") + "  - Webhook management and integration endpoints")
	fmt.Println()

	// Infrastructure & Data
	fmt.Println(helpers.TitleStyle.Render("🏗️ Infrastructure & Data"))
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " " + helpers.CmdDescStyle.Render("network") + " - Network diagnostics, policies, and connectivity testing")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " " + helpers.CmdDescStyle.Render("db") + "      - Database management, operations, and monitoring")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " " + helpers.CmdDescStyle.Render("storage") + " - Storage management, volumes, and data persistence")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " " + helpers.CmdDescStyle.Render("service") + " - Service management, load balancing, and API endpoints")
	fmt.Println()

	// Resource Management & Optimization
	fmt.Println(helpers.TitleStyle.Render("📊 Resource Management & Optimization"))
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " " + helpers.CmdDescStyle.Render("scale") + "   - Resource scaling, auto-scaling, and optimization")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " " + helpers.CmdDescStyle.Render("backup") + "  - Platform backup creation and management")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " " + helpers.CmdDescStyle.Render("restore") + " - Platform restoration from backups")
	fmt.Println()

	// Utilities
	fmt.Println(helpers.TitleStyle.Render("🛠️ Utilities"))
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " " + helpers.CmdDescStyle.Render("help") + "     - Get help about commands (this command)")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " " + helpers.CmdDescStyle.Render("version") + "  - Show version information and build details")
	fmt.Println()

	// Quick Start Examples
	fmt.Println(helpers.TitleStyle.Render("🚀 Quick Start Examples"))
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " " + helpers.CmdDescStyle.Render("adhar up") + "                    - Create local development platform")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " " + helpers.CmdDescStyle.Render("adhar up -f config.yaml") + "   - Deploy production platform")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " " + helpers.CmdDescStyle.Render("adhar get status") + "          - Check platform health")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " " + helpers.CmdDescStyle.Render("adhar get secrets") + "         - View platform credentials")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " " + helpers.CmdDescStyle.Render("adhar down") + "                - Clean up local platform")
	fmt.Println()

	// Help Information
	fmt.Println(helpers.TitleStyle.Render("📚 Getting More Help"))
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " " + helpers.InfoStyle.Render("Command-specific help:") + " " + helpers.HighlightStyle.Render("adhar help <command>"))
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " " + helpers.InfoStyle.Render("Detailed documentation:") + " " + helpers.HighlightStyle.Render("https://docs.adhar.io"))
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " " + helpers.InfoStyle.Render("Community support:") + " " + helpers.HighlightStyle.Render("https://github.com/adhar-io/adhar"))
	fmt.Println()

	// Tips
	fmt.Println(helpers.TitleStyle.Render("💡 Pro Tips"))
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " Use " + helpers.HighlightStyle.Render("--dry-run") + " flag to preview changes without applying them")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " Use " + helpers.HighlightStyle.Render("--verbose") + " flag for detailed output and debugging")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " Use " + helpers.HighlightStyle.Render("--no-color") + " flag to disable colored output")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " Use " + helpers.HighlightStyle.Render("--kubeconfig") + " to specify custom kubeconfig path")
	fmt.Println()
}
