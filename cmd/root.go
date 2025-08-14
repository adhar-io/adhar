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

	"github.com/charmbracelet/glamour"
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

// GlamourStyles defines the styles to use for rendering markdown
var glamourStyles = map[string]glamour.TermRendererOption{
	"light":   glamour.WithStandardStyle("light"),   // Use standard light style
	"dark":    glamour.WithStandardStyle("dark"),    // Use standard dark style
	"dracula": glamour.WithStandardStyle("dracula"), // Add dracula style if needed
	"auto":    glamour.WithAutoStyle(),
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

// renderMarkdown renders markdown text with glamour with enhanced styling options
func renderMarkdown(md string, style string) (string, error) {
	rendererStyle, exists := glamourStyles[style]
	if !exists {
		// Fallback to auto style if the requested style doesn't exist
		rendererStyle = glamourStyles["auto"]
	}

	renderer, err := glamour.NewTermRenderer(
		rendererStyle,
		glamour.WithWordWrap(100),
		glamour.WithEmoji(),
	)
	if err != nil {
		return "", err
	}
	return renderer.Render(md)
}

// renderCommands renders a list of commands in a pretty format
func renderCommands(commands []*cobra.Command) string {
	var sb strings.Builder

	for _, cmd := range commands {
		name := titleStyle.Render(cmd.Name())
		desc := cmdDescStyle.Render(cmd.Short)
		sb.WriteString(fmt.Sprintf("%s\n  %s\n\n", name, desc))
	}

	return sb.String()
}

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:     "adhar",
	Aliases: []string{"a", "ad"},
	Short:   "Adhar Platform - The Open Foundation for your Internal Developer Platform",
	Long: lipgloss.JoinVertical(lipgloss.Left,
		"",
		highlightStyle.Render("Adhar streamlines your entire software development lifecycle across five key stages:"),
		"",
		highlightStyle.Render("Define:")+" Plan and structure your projects.",
		bulletStyle.Render("  • "+"Define your business requirements"),
		bulletStyle.Render("  • "+"Define your goals and objectives"),
		bulletStyle.Render("  • "+"Define your processes and workflows"),
		bulletStyle.Render("  • "+"Define your success criteria"),
		"",
		highlightStyle.Render("Design:")+" Architect and scaffold your applications.",
		bulletStyle.Render("  • "+"Design your UI/UX journey with low-code tools"),
		bulletStyle.Render("  • "+"Design your technology architecture with best practices"),
		bulletStyle.Render("  • "+"Create apps & services instantly with templates"),
		bulletStyle.Render("  • "+"Design your data architecture with best practices"),
		bulletStyle.Render("  • ")+"Design your UI/UX journey with low-code tools",
		bulletStyle.Render("  • ")+"Design your technology architecture with best practices",
		bulletStyle.Render("  • ")+"Create apps & services instantly with templates",
		bulletStyle.Render("  • ")+"Design your data architecture with best practices",
		"",
		bulletStyle.Render("  • "+"Collaborate with your team and stakeholders"),
		"",
		highlightStyle.Render("Deliver:")+" Ship, manage, and secure your deployments.",
		bulletStyle.Render("  • "+"Ship confidently with GitOps to any environment"),
		bulletStyle.Render("  • "+"Control pipelines, infrastructure, and secrets easily"),
		bulletStyle.Render("  • "+"Keep your applications and data safe"),
		bulletStyle.Render("  • "+"Manage your applications and services effortlessly"),
		bulletStyle.Render("  • ")+"Ship confidently with GitOps to any environment",
		bulletStyle.Render("  • ")+"Control pipelines, infrastructure, and secrets easily",
		bulletStyle.Render("  • ")+"Keep your applications and data safe",
		bulletStyle.Render("  • ")+"Manage your applications and services effortlessly",
		"",
		highlightStyle.Render("Discover:")+" Monitor, optimize, and gain insights.",
		bulletStyle.Render("  • ")+"Gain insights with built-in monitoring",
		bulletStyle.Render("  • ")+"Optimize performance effortlessly",
		bulletStyle.Render("  • ")+"Discover and manage your business metrics",
		bulletStyle.Render("  • ")+"Discover and manage your data",
		"",
		highlightStyle.Render("Decide:")+" Actionable Insights, Data Driven Decisions",
		bulletStyle.Render("  • ")+"Find out actionable insights from your data",
		bulletStyle.Render("  • ")+"Data driven decisions",
		bulletStyle.Render("  • ")+"Spent time and effort where it matters",
		bulletStyle.Render("  • ")+"Make the right decisions",
		"",
		highlightStyle.Render("Unlock developer productivity and maintain control!"),
		"",
	),
	Example: lipgloss.JoinVertical(lipgloss.Left,
		infoStyle.Render("  # Create a new application"),
		"  "+highlightStyle.Render("adhar apps create myorg myspace my-api --template spring-boot-api"),
		"",
		infoStyle.Render("  # Deploy an application"),
		"  "+highlightStyle.Render("adhar apps deploy myorg myspace my-api --image=company/my-api:latest"),
		"",
		infoStyle.Render("  # List all applications"),
		"  "+highlightStyle.Render("adhar apps list myorg myspace"),
		"",
		infoStyle.Render("  # Get platform status"),
		"  "+highlightStyle.Render("adhar get status"),
	),
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		// Print header before any command runs
		// Skip header for help command itself to avoid duplication with Cobra's default help flag behavior
		if cmd.Name() != "help" && cmd.Name() != "__complete" && cmd.Name() != "__completeNoDesc" { // Avoid printing for built-in completion commands too
			// Check if the --help flag was used
			helpFlag, _ := cmd.Flags().GetBool("help")
			if !helpFlag {
				printHeader()
			}
		}
	},
	Run: func(cmd *cobra.Command, args []string) {
		// Show a welcome message
		welcomeMsg := highlightStyle.Render("Welcome to Adhar Platform!") + "\n\n" +
			infoStyle.Render("Unlock developer productivity and maintain control!")
		fmt.Println(welcomeMsg)
		fmt.Println()

		// Render main description as markdown
		description := `
			## About Adhar

			Adhar is an open foundation for your Internal Developer Platform that streamlines your entire software development lifecycle across multiple stages.

			### Main Features:

			* **Define** - Plan and structure your projects, requirements, goals, processes, and success criteria
			* **Design** - Architect and scaffold your applications with low-code tools and best practices
			* **Deliver** - Ship, manage, and secure your deployments with GitOps, pipeline controls, and security measures
			* **Discover** - Monitor, optimize, and gain insights into your applications and business metrics
			* **Cloud Native** - Built on Kubernetes and Crossplane with a focus on developer productivity
			* **GitOps Ready** - Declarative configuration and management across environments
		`

		rendered, err := renderMarkdown(description, "auto")
		if err == nil {
			fmt.Println(rendered)
		} else {
			// Fallback if markdown rendering fails
			fmt.Println(cmd.Long)
		}

		// Render available commands in a fancy box
		fmt.Println(headerStyle.Render("AVAILABLE COMMANDS:"))
		commandsBox := borderStyle.Render(renderCommands(cmd.Commands()))
		fmt.Println(commandsBox)

		// Show some helpful tips
		tips := `
			💡 Tip: Use 'adhar [command] --help' for more information about a specific command.
			🔄 Run 'adhar up -e <environment>' to provision a new environment.
			🌐 First time user? Try 'adhar help' to see detailed documentation.
			🔍 Use 'adhar get all' to see all resources managed by Adhar.
		`
		fmt.Println(headerStyle.Render("TIPS & TRICKS:"))
		fmt.Println(lipgloss.NewStyle().Faint(true).Render(tips))

		// Show support and community info
		community := `
			Community: https://github.com/adhar-io/adhar/community
			Documentation: https://docs.adhar.io
			Issues: https://github.com/adhar-io/adhar/issues
		`
		fmt.Println(headerStyle.Render("COMMUNITY & SUPPORT:"))
		fmt.Println(infoStyle.Render(community))
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

	// Added log level and colored output flags
	rootCmd.PersistentFlags().StringVar(&logger.CLILogLevel, "log-level", "info", logger.LogLevelMsg)
	rootCmd.PersistentFlags().BoolVar(&logger.CLIColoredOutput, "colored-logs", true, logger.ColoredOutputMsg)
}

// AddCommand adds one or more commands to the root command
func AddCommand(cmd ...*cobra.Command) {
	rootCmd.AddCommand(cmd...)
}
