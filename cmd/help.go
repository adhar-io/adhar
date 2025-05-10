package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

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
			// Main description
			description := `
Adhar is a modern tool for managing application environments and deployments via Kubernetes and Crossplane.
It simplifies environment provisioning, application deployment, and resource management.
`
			fmt.Println(description)

			// Create sections for commands
			fmt.Println(headerStyle.Render("COMMAND CATEGORIES:"))

			// Define command categories (Updated)
			commandCategories := map[string][]string{
				"Application Lifecycle":  {"apps"}, // Added apps
				"Environment Management": {"up", "down"},
				"Resource Management":    {"get"},             // 'get' covers multiple resources now
				"Platform Status":        {"status"},          // Added status command here
				"System":                 {"version", "help"}, // Removed explore
			}

			// Display command categories
			var sb strings.Builder
			for category, cmdList := range commandCategories {
				sb.WriteString(fmt.Sprintf("%s\n", titleStyle.Render(category)))

				for _, cmdName := range cmdList {
					// Find command by name (search root and subcommands)
					var helpText string
					foundCmd, _, err := rootCmd.Find([]string{cmdName}) // Use Find for better searching
					if err == nil && foundCmd != nil {
						helpText = foundCmd.Short
					} else {
						helpText = "Command description not found" // Fallback
					}

					sb.WriteString(fmt.Sprintf("  %s: %s\n",
						subtitleStyle.Render(cmdName),
						helpText))
				}
				sb.WriteString("\n")
			}

			categoryBox := borderStyle.Render(sb.String())
			fmt.Println(categoryBox)

			// Show usage examples (Updated)
			fmt.Println(headerStyle.Render("COMMON USAGE:"))
			examples := `
  # Start the local environment (Kind cluster)
  adhar up

  # Provision a specific environment defined in config (e.g., GKE)
  adhar up -e staging-gke

  # List all Adhar applications
  adhar get applications

  # Get the status of the Adhar platform components
  adhar get status

  # Show detailed help for the 'get' command
  adhar help get

  # Display version information
  adhar version

  # Tear down the local environment
  adhar down

  # Tear down a specific environment
  adhar down -e staging-gke
`
			examplesBox := borderStyle.Render(examples)
			fmt.Println(examplesBox)

			// Add tips section
			tips := `
💡 Use 'adhar help <command>' for detailed information on a specific command
💡 All commands support the --help flag for quick reference
💡 Use TAB completion (if enabled in your shell) to quickly navigate commands and options
💡 Use 'adhar get all' to list all managed Adhar resource types
💡 Specify a kubeconfig using --kubeconfig flag or KUBECONFIG environment variable
`
			fmt.Println(subtitleStyle.Render("TIPS:"))
			fmt.Println(lipgloss.NewStyle().Faint(true).Render(tips))
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
