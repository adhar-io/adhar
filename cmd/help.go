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

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"adhar-io/adhar/globals"
)

// GlamourStyles defines the styles to use for rendering markdown
var glamourStyles = map[string]glamour.TermRendererOption{
	"light":   glamour.WithStandardStyle("light"),   // Use standard light style
	"dark":    glamour.WithStandardStyle("dark"),    // Use standard dark style
	"dracula": glamour.WithStandardStyle("dracula"), // Add dracula style if needed
	"auto":    glamour.WithAutoStyle(),
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

// renderRootCommandContent renders the main content shown when running 'adhar' without arguments
// This function is shared between the root command and help command for consistency
func renderRootCommandContent(cmd *cobra.Command) {
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
}

func init() {
	// Add the help command to the root command
	AddCommand(helpCmd)
}

// helpCmd represents the help command - an enhanced version of built-in help
var helpCmd = &cobra.Command{
	Use:   "help [command]",
	Short: "Get help on any command",
	Long: `An enhanced help system for Adhar Platform with additional information and examples.
When provided with a command name, it shows detailed help for that command.
Without a command name, it displays general help for Adhar.`, // Header is now printed by PersistentPreRun in root.go
	Run: func(cmd *cobra.Command, args []string) {
		// Print the header with ASCII art and version
		printHeader()

		if len(args) == 0 {
			// Use the same beautiful content as the root command
			renderRootCommandContent(rootCmd)
		} else {
			// Show help for a specific command
			subcommandName := args[0]

			// Find the command using Cobra's Find method for better accuracy
			targetCmd, _, err := rootCmd.Find(args)
			if err != nil || targetCmd == nil {
				fmt.Printf("%s Command '%s' not found. Run 'adhar help' to see available commands.\n",
					errorStyle.Render("Error:"),
					subcommandName)
				os.Exit(1)
			}

			// Use centralized header for command-specific help
			renderCommandHeader("HELP: "+strings.ToUpper(targetCmd.CommandPath()), targetCmd.Short)

			// Command usage
			if targetCmd.Runnable() {
				fmt.Printf("  adhar %s\n\n", targetCmd.UseLine()) // Use UseLine for better format
			} else {
				fmt.Printf("  adhar %s [command]\n\n", targetCmd.CommandPath())
			}

			// Command description
			if targetCmd.Long != "" {
				fmt.Println(titleStyle.Render("Description:"))
				// Render markdown if possible
				renderedDesc, renderErr := renderMarkdown(targetCmd.Long, "auto")
				if renderErr == nil {
					fmt.Println(borderStyle.Render(renderedDesc))
				} else {
					fmt.Println(borderStyle.Render(targetCmd.Long)) // Fallback to plain text
				}
			} else if targetCmd.Short != "" {
				// Fallback to short description if long is empty
				fmt.Println(titleStyle.Render("Description:"))
				fmt.Println(borderStyle.Render(targetCmd.Short))
			}

			// Command flags (including persistent flags from parents)
			if targetCmd.HasAvailableFlags() {
				fmt.Println(titleStyle.Render("Flags:"))
				var flagsBuilder strings.Builder
				targetCmd.Flags().VisitAll(func(flag *pflag.Flag) {
					// Skip help flag
					if flag.Name == "help" {
						return
					}
					// Get flag shorthand and name
					name := "--" + flag.Name
					if flag.Shorthand != "" {
						name = fmt.Sprintf("-%s, %s", flag.Shorthand, name)
					}

					// Add padding for alignment
					paddedName := fmt.Sprintf("%-25s", name) // Increased padding
					flagsBuilder.WriteString(fmt.Sprintf("  %s %s",
						subtitleStyle.Render(paddedName),
						flag.Usage))
					// Add default value if not empty and not sensitive
					if flag.DefValue != "" && flag.Name != "kubeconfig" { // Example: hide default for kubeconfig if desired
						flagsBuilder.WriteString(fmt.Sprintf(" (default: %s)", flag.DefValue))
					}
					flagsBuilder.WriteString("\n")
				})

				// Include persistent flags from parents unless already shown
				targetCmd.InheritedFlags().VisitAll(func(flag *pflag.Flag) {
					// Skip help flag and flags already defined locally
					if flag.Name == "help" || targetCmd.Flags().Lookup(flag.Name) != nil {
						return
					}
					name := "--" + flag.Name
					if flag.Shorthand != "" {
						name = fmt.Sprintf("-%s, %s", flag.Shorthand, name)
					}
					paddedName := fmt.Sprintf("%-25s", name)
					flagsBuilder.WriteString(fmt.Sprintf("  %s %s",
						subtitleStyle.Render(paddedName),
						flag.Usage))
					if flag.DefValue != "" {
						flagsBuilder.WriteString(fmt.Sprintf(" (default: %s)", flag.DefValue))
					}
					flagsBuilder.WriteString(" (persistent)\n") // Indicate it's a persistent flag
				})

				if flagsBuilder.Len() > 0 {
					fmt.Println(borderStyle.Render(flagsBuilder.String()))
				} else {
					fmt.Println("  No flags available for this command")
				}
			}

			// Show aliases if any
			if len(targetCmd.Aliases) > 0 {
				fmt.Println(titleStyle.Render("Aliases:"))
				fmt.Printf("  %s\n\n", strings.Join(targetCmd.Aliases, ", "))
			}

			// Show examples based on the command (Updated)
			fmt.Println(titleStyle.Render("Examples:"))

			examples := targetCmd.Example // Use Example field if available
			if examples == "" {           // Fallback to switch statement if Example field is empty
				switch targetCmd.Name() {
				case "up":
					examples = `  # Start a local Kind cluster and controller manager
  adhar up

  # Provision the 'staging' environment defined in adhar-config.yaml
  adhar up -e staging

  # Start and wait for all components to be ready
  adhar up --wait`
				case "down":
					examples = `  # Tear down the local Adhar environment (Kind cluster)
  adhar down

  # Tear down the 'staging' environment (removes Crossplane resources)
  adhar down -e staging

  # Force tear down even if resources are still in use (use with caution)
  adhar down --force`
				case "get":
					examples = `  # List all Adhar applications in the default namespace
  adhar get applications

  # Get a specific database named 'my-db'
  adhar get database my-db

  # List all Adhar environments across all namespaces
  adhar get environments -A

  # List all Adhar resources (apps, dbs, envs, etc.)
  adhar get all

  # Get the status of Adhar platform components
  adhar get status

  # Get resources using a specific kubeconfig file
  adhar get applications --kubeconfig ~/.kube/staging-config`
				case "status": // Added specific example for status
					examples = `  # Check the status of the Adhar controller manager
  adhar get status

  # Check status using a specific kubeconfig
  adhar get status --kubeconfig ~/.kube/other-config`
				case "version":
					examples = `  # Display version information
  adhar version`
				default:
					// Check if it's a subcommand like 'applications' under 'get'
					if targetCmd.Parent() != nil && targetCmd.Parent().Name() == "get" {
						resourceType := targetCmd.Name()
						if strings.HasSuffix(resourceType, "s") { // Use plural form
							examples = fmt.Sprintf(`  # List all %s
  adhar get %s

  # Get a specific %s named 'my-%s'
  adhar get %s my-%s

  # List %s in JSON format
  adhar get %s -o json`, resourceType, resourceType, targetCmd.Aliases[0], targetCmd.Aliases[0], resourceType, targetCmd.Aliases[0], resourceType, resourceType)
						} else {
							examples = fmt.Sprintf("  # Get %s resources\n  adhar get %s [resource-name]", resourceType, resourceType)
						}
					} else {
						examples = "  No specific examples available for this command."
					}
				}
			}

			fmt.Println(borderStyle.Render(strings.TrimSpace(examples))) // Trim space for cleaner look
		}
	},
}
