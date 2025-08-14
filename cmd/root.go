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

// Command groups for better organization
type CommandGroup struct {
	Name        string
	Description string
	Commands    []string
	Icon        string
}

var commandGroups = []CommandGroup{
	{
		Name:        "Platform Operations",
		Description: "Core platform lifecycle management",
		Commands:    []string{"up", "down", "cluster"},
		Icon:        "🏗️",
	},
	{
		Name:        "Application Management",
		Description: "Develop, deploy, and manage applications",
		Commands:    []string{"apps"},
		Icon:        "🚀",
	},
	{
		Name:        "Resources & Status",
		Description: "View and monitor platform resources",
		Commands:    []string{"get"},
		Icon:        "📊",
	},
	{
		Name:        "Utilities",
		Description: "Additional tools and utilities",
		Commands:    []string{"completion", "help", "version"},
		Icon:        "🛠️",
	},
}

// renderCommandGroups renders commands organized by groups
func renderCommandGroups(commands []*cobra.Command) string {
	var sb strings.Builder

	// Create a map of commands by name for quick lookup
	cmdMap := make(map[string]*cobra.Command)
	for _, cmd := range commands {
		cmdMap[cmd.Name()] = cmd
	}

	for i, group := range commandGroups {
		if i > 0 {
			sb.WriteString("\n")
		}

		// Group header
		groupHeader := fmt.Sprintf("%s %s", group.Icon, group.Name)
		sb.WriteString(headerStyle.Render(groupHeader) + "\n")
		sb.WriteString(subtitleStyle.Render(group.Description) + "\n\n")

		// Commands in this group
		for _, cmdName := range group.Commands {
			if cmd, exists := cmdMap[cmdName]; exists {
				cmdLine := fmt.Sprintf("  %s  %s",
					titleStyle.Render(cmd.Name()),
					cmdDescStyle.Render(cmd.Short))
				sb.WriteString(cmdLine + "\n")
			}
		}
	}

	return sb.String()
}

// renderQuickStart provides a quick start guide
func renderQuickStart() string {
	quickStart := `
**Quick Start Guide:**

1. **Start a local development environment:**
   ` + "`adhar up`" + `

2. **Deploy your first application:**
   ` + "`adhar apps create myorg myapp my-service --template node-express`" + `

3. **Check platform status:**
   ` + "`adhar get status`" + `

4. **Clean up when done:**
   ` + "`adhar down`" + `
`

	rendered, err := renderMarkdown(quickStart, "auto")
	if err != nil {
		return quickStart
	}
	return rendered
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
				printHeader()
			}
		}
	},
	Run: func(cmd *cobra.Command, args []string) {
		// Modern welcome with better visual hierarchy
		welcomeHeader := borderStyle.Width(80).Align(lipgloss.Center).Render(
			highlightStyle.Render("🚀 Welcome to Adhar Platform") + "\n" +
				subtitleStyle.Render("The Open Foundation for Internal Developer Platforms"),
		)
		fmt.Println(welcomeHeader)
		fmt.Println()

		// Platform overview with styled formatting for better control
		overviewHeader := highlightStyle.Render("Adhar") + " transforms how teams build and deploy software by providing:"
		fmt.Println(overviewHeader)
		fmt.Println()

		// Use direct styling for bullet points to ensure proper line breaks
		features := []string{
			"🏗️ Unified Platform - Complete IDP on Kubernetes with GitOps",
			"⚡ Developer Velocity - Templates, automation, and self-service",
			"🔒 Enterprise Ready - Security, governance, and compliance built-in",
			"🌐 Multi-Cloud - Deploy anywhere with consistent experience",
			"📊 Full Observability - Monitor everything from code to production",
		}

		for _, feature := range features {
			fmt.Printf("  %s %s\n", bulletStyle.Render("•"), feature)
		}
		fmt.Println()

		// Commands organized by groups
		fmt.Println(headerStyle.Render("📋 AVAILABLE COMMANDS"))
		commandsContent := renderCommandGroups(cmd.Commands())
		commandsBox := borderStyle.Width(80).Render(commandsContent)
		fmt.Println(commandsBox)
		fmt.Println()

		// Quick start guide
		fmt.Println(headerStyle.Render("🚀 QUICK START"))
		quickStartBox := borderStyle.Width(80).Render(renderQuickStart())
		fmt.Println(quickStartBox)
		fmt.Println()

		// Help and resources in a compact format
		resources := []string{
			"💡 Get help: " + highlightStyle.Render("adhar [command] --help"),
			"📚 Documentation: " + infoStyle.Render("https://adhar.io/docs"),
			"💬 Community: " + infoStyle.Render("https://github.com/adhar-io/adhar/community"),
			"🐛 Issues: " + infoStyle.Render("https://github.com/adhar-io/adhar/issues"),
		}

		fmt.Println(headerStyle.Render("🔗 RESOURCES & SUPPORT"))
		for _, resource := range resources {
			fmt.Println("  " + resource)
		}
		fmt.Println()

		// Footer with version info
		footer := subtitleStyle.Render(
			fmt.Sprintf("Adhar Platform %s • Built with ❤️ for developers", globals.Version),
		)
		fmt.Println(lipgloss.NewStyle().Align(lipgloss.Center).Render(footer))
		fmt.Println()
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
