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

package help

import (
	"fmt"
	"strings"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/globals"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// HelpCmd represents the help command
var HelpCmd = &cobra.Command{
	Use:   "help [command]",
	Short: "Get help on any command",
	Long: `Help provides comprehensive assistance for Adhar Platform commands.

Use 'adhar help [command]' to get detailed information about a specific command.
Use 'adhar help' to see an overview of all available commands.`,
	Run: func(cmd *cobra.Command, args []string) {
		if len(args) == 0 {
			// Show comprehensive help overview
			showComprehensiveHelp()
		} else {
			// Show help for a specific command
			showCommandHelp(cmd, args)
		}
	},
}

// showComprehensiveHelp displays a comprehensive overview of all commands
func showComprehensiveHelp() {
	// Show header (ASCII art and platform info)
	showHeader()

	// Show platform description
	fmt.Println(helpers.TitleStyle.Render("🚀 Adhar Platform - Complete Command Reference"))
	fmt.Println()
	fmt.Println(helpers.InfoStyle.Render("Adhar Platform is a comprehensive Internal Developer Platform (IDP) that provides"))
	fmt.Println(helpers.InfoStyle.Render("unified Kubernetes-native approach for the entire software development lifecycle."))
	fmt.Println()

	// Show platform overview
	showPlatformOverview()
	fmt.Println()

	// Platform Lifecycle
	fmt.Println(helpers.HeaderStyle.Render("📋 Platform Lifecycle"))
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " up       - Create and start the Adhar platform (local or cloud)")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " down     - Stop and destroy the Adhar platform")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " status   - Check platform health and status")
	fmt.Println()

	// Resource Management
	fmt.Println(helpers.HeaderStyle.Render("🔍 Resource Management"))
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " get       - Display platform resources (apps, secrets, clusters, etc.)")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " apps     - Manage application lifecycle and deployments")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " cluster  - Manage Kubernetes clusters and configurations")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " config   - Manage platform configuration and settings")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " env      - Manage platform environments (dev, staging, prod)")
	fmt.Println()

	// Operations & Monitoring
	fmt.Println(helpers.HeaderStyle.Render("⚡ Operations & Monitoring"))
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " health   - Check platform health, services, and dependencies")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " logs     - View centralized platform logs and events")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " metrics  - Access platform metrics and performance data")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " traces   - View distributed tracing information")
	fmt.Println()

	// Security & Compliance
	fmt.Println(helpers.HeaderStyle.Render("🔒 Security & Compliance"))
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " security - Security scanning, vulnerability management, and policies")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " auth     - Authentication, authorization, and user management")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " secrets  - Manage secrets, certificates, and sensitive data")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " policy   - Platform policy management and governance")
	fmt.Println()

	// GitOps & DevOps
	fmt.Println(helpers.HeaderStyle.Render("🔄 GitOps & DevOps"))
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " gitops  - GitOps operations, ArgoCD management, and workflows")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " pipeline - CI/CD pipeline creation, execution, and management")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " webhook - Webhook management and integration endpoints")
	fmt.Println()

	// Infrastructure & Data
	fmt.Println(helpers.HeaderStyle.Render("🏗️ Infrastructure & Data"))
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " network - Network diagnostics, policies, and connectivity testing")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " db      - Database management, operations, and monitoring")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " storage - Storage management, volumes, and data persistence")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " service - Service management, load balancing, and API endpoints")
	fmt.Println()

	// Resource Management & Optimization
	fmt.Println(helpers.HeaderStyle.Render("📊 Resource Management & Optimization"))
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " scale   - Resource scaling, auto-scaling, and optimization")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " backup  - Platform backup creation and management")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " restore - Platform restoration from backups")
	fmt.Println()

	// Utilities
	fmt.Println(helpers.HeaderStyle.Render("🛠️ Utilities"))
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " help     - Get help about commands (this command)")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " version  - Show version information and build details")
	fmt.Println()

	// Quick Start Examples
	fmt.Println(helpers.HeaderStyle.Render("🚀 Quick Start Examples"))
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " adhar up                    - Create local development platform")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " adhar up -f config.yaml   - Deploy production platform")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " adhar get status          - Check platform health")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " adhar get secrets         - View platform credentials")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " adhar down                - Clean up local platform")
	fmt.Println()

	// Help Information
	fmt.Println(helpers.HeaderStyle.Render("📚 Getting More Help"))
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " " + helpers.InfoStyle.Render("Command-specific help:") + " adhar help <command>")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " " + helpers.InfoStyle.Render("Detailed documentation:") + " " + helpers.UrlStyle.Render("https://docs.adhar.io"))
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " " + helpers.InfoStyle.Render("Community support:") + " " + helpers.UrlStyle.Render("https://github.com/adhar-io/adhar"))
	fmt.Println()

	// Tips
	fmt.Println(helpers.HeaderStyle.Render("💡 Pro Tips"))
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " Use --dry-run flag to preview changes without applying them")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " Use --verbose flag for detailed output and debugging")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " Use --no-color flag to disable colored output")
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " Use --kubeconfig to specify custom kubeconfig path")
	fmt.Println()

	// Show footer
	showFooter()
}

// showCommandHelp displays help for a specific command
func showCommandHelp(cmd *cobra.Command, args []string) {
	commandName := args[0]

	// Find the command in the root command
	targetCmd, _, err := cmd.Root().Find(args)
	if err != nil {
		fmt.Printf("%s Command '%s' not found.\n", helpers.ErrorStyle.Render("❌"), commandName)
		fmt.Println()
		fmt.Println("Available commands:")
		fmt.Println("  " + helpers.CodeStyle.Render("up, down, get, apps, cluster, config, env, health, logs"))
		fmt.Println("  " + helpers.CodeStyle.Render("security, auth, secrets, policy, gitops, pipeline, webhook"))
		fmt.Println("  " + helpers.CodeStyle.Render("network, db, metrics, traces, storage, service, scale"))
		fmt.Println("  " + helpers.CodeStyle.Render("backup, restore, help, version"))
		fmt.Println()
		fmt.Printf("Use %s to see all available commands.\n", helpers.CodeStyle.Render("adhar help"))
		return
	}

	// Show command-specific help
	fmt.Printf("%s Help for: %s\n", helpers.TitleStyle.Render("📖"), commandName)
	fmt.Println()

	// Show command description
	if targetCmd.Long != "" {
		cleanDescription := cleanCommandDescription(targetCmd.Long)
		fmt.Println(helpers.InfoStyle.Render(cleanDescription))
		fmt.Println()
	} else if targetCmd.Short != "" {
		cleanDescription := cleanCommandDescription(targetCmd.Short)
		fmt.Println(helpers.InfoStyle.Render(cleanDescription))
		fmt.Println()
	}

	// Show usage
	if targetCmd.Use != "" {
		fmt.Printf("%s Usage:\n", helpers.HeaderStyle.Render("📝"))
		fmt.Printf("  adhar %s\n", targetCmd.Use)
		fmt.Println()
	}

	// Show aliases
	if len(targetCmd.Aliases) > 0 {
		fmt.Printf("%s Aliases:\n", helpers.HeaderStyle.Render("🔗"))
		for _, alias := range targetCmd.Aliases {
			fmt.Printf("  %s\n", alias)
		}
		fmt.Println()
	}

	// Show flags
	if targetCmd.Flags().HasFlags() || targetCmd.PersistentFlags().HasFlags() {
		fmt.Printf("%s Available Flags:\n", helpers.HeaderStyle.Render("⚙️"))

		// Show persistent flags first
		if targetCmd.PersistentFlags().HasFlags() {
			fmt.Println("  " + helpers.SubtitleStyle.Render("Global Flags:"))
			targetCmd.PersistentFlags().VisitAll(func(flag *pflag.Flag) {
				if flag.Shorthand != "" {
					fmt.Printf("    -%s, --%s\t%s\n", flag.Shorthand, flag.Name, flag.Usage)
				} else {
					fmt.Printf("    --%s\t%s\n", flag.Name, flag.Usage)
				}
			})
			fmt.Println()
		}

		// Show local flags
		if targetCmd.Flags().HasFlags() {
			fmt.Println("  " + helpers.SubtitleStyle.Render("Command Flags:"))
			targetCmd.Flags().VisitAll(func(flag *pflag.Flag) {
				if flag.Shorthand != "" {
					fmt.Printf("    -%s, --%s\t%s\n", flag.Shorthand, flag.Name, flag.Usage)
				} else {
					fmt.Printf("    --%s\t%s\n", flag.Name, flag.Usage)
				}
			})
			fmt.Println()
		}
	}

	// Show examples if available
	if targetCmd.Example != "" {
		fmt.Printf("%s Examples:\n", helpers.HeaderStyle.Render("💡"))
		fmt.Println(targetCmd.Example)
		fmt.Println()
	}

	// Show subcommands if available
	if len(targetCmd.Commands()) > 0 {
		fmt.Printf("%s Subcommands:\n", helpers.HeaderStyle.Render("📚"))
		for _, subcmd := range targetCmd.Commands() {
			if !subcmd.IsAvailableCommand() || subcmd.IsAdditionalHelpTopicCommand() {
				continue
			}
			fmt.Printf("  %s\t%s\n", subcmd.Name(), subcmd.Short)
		}
		fmt.Println()
	}

	// Show additional help information
	fmt.Printf("%s Additional Help:\n", helpers.HeaderStyle.Render("🔍"))
	fmt.Printf("  • Run adhar %s --help for detailed flag information\n", commandName)
	fmt.Printf("  • Visit %s for comprehensive documentation\n", helpers.UrlStyle.Render("https://docs.adhar.io"))
	fmt.Println()

	// Show related commands
	showRelatedCommands(commandName)
}

// showRelatedCommands suggests related commands based on the current command
func showRelatedCommands(commandName string) {
	relatedCommands := map[string][]string{
		"up":       {"down", "status", "get", "health"},
		"down":     {"up", "status", "get"},
		"get":      {"status", "health", "logs", "metrics"},
		"apps":     {"get", "cluster", "config", "gitops"},
		"cluster":  {"get", "config", "up", "down"},
		"config":   {"get", "cluster", "env"},
		"env":      {"config", "cluster", "get"},
		"health":   {"get", "logs", "metrics", "status"},
		"logs":     {"health", "metrics", "get"},
		"security": {"auth", "secrets", "policy", "compliance"},
		"auth":     {"security", "secrets", "policy"},
		"secrets":  {"auth", "security", "get"},
		"gitops":   {"apps", "cluster", "config"},
		"pipeline": {"gitops", "apps", "webhook"},
		"network":  {"health", "cluster", "get"},
		"db":       {"storage", "backup", "restore"},
		"metrics":  {"health", "logs", "traces"},
		"traces":   {"metrics", "logs", "health"},
		"storage":  {"db", "backup", "restore"},
		"service":  {"apps", "cluster", "network"},
		"scale":    {"cluster", "apps", "health"},
		"backup":   {"restore", "storage", "db"},
		"restore":  {"backup", "storage", "db"},
		"policy":   {"security", "auth", "secrets"},
		"webhook":  {"pipeline", "gitops", "apps"},
	}

	if related, exists := relatedCommands[commandName]; exists {
		fmt.Printf("%s Related Commands:\n", helpers.HeaderStyle.Render("🔗"))
		for _, cmd := range related {
			fmt.Printf("  %s\n", cmd)
		}
		fmt.Println()
	}
}

// getCommandDescription returns a user-friendly description for a command
//
//nolint:unused // Reserved for future interactive help output
func getCommandDescription(commandName string) string {
	descriptions := map[string]string{
		"up":       "Create and start the Adhar platform (local or cloud-based)",
		"down":     "Stop and destroy the Adhar platform",
		"get":      "Display platform resources and information",
		"apps":     "Manage application lifecycle and deployments",
		"cluster":  "Manage Kubernetes clusters and configurations",
		"config":   "Manage platform configuration and settings",
		"env":      "Manage platform environments (dev, staging, prod)",
		"health":   "Check platform health, services, and dependencies",
		"logs":     "View centralized platform logs and events",
		"security": "Security scanning, vulnerability management, and policies",
		"auth":     "Authentication, authorization, and user management",
		"secrets":  "Manage secrets, certificates, and sensitive data",
		"gitops":   "GitOps operations, ArgoCD management, and workflows",
		"pipeline": "CI/CD pipeline creation, execution, and management",
		"webhook":  "Webhook management and integration endpoints",
		"network":  "Network diagnostics, policies, and connectivity testing",
		"db":       "Database management, operations, and monitoring",
		"metrics":  "Access platform metrics and performance data",
		"traces":   "View distributed tracing information",
		"storage":  "Storage management, volumes, and data persistence",
		"service":  "Service management, load balancing, and API endpoints",
		"scale":    "Resource scaling, auto-scaling, and optimization",
		"backup":   "Platform backup creation and management",
		"restore":  "Platform restoration from backups",
		"policy":   "Platform policy management and governance",
		"help":     "Get help about commands",
		"version":  "Show version information and build details",
	}

	if desc, exists := descriptions[commandName]; exists {
		return desc
	}
	return "Command description not available"
}

// cleanCommandDescription removes box characters and cleans up formatting issues
func cleanCommandDescription(description string) string {
	if description == "" {
		return description
	}

	// Remove box characters and unnecessary formatting
	lines := strings.Split(description, "\n")
	var cleanedLines []string
	var lastLineEmpty bool

	for _, line := range lines {
		// Clean up the line
		cleanedLine := cleanLine(line)

		// Skip empty lines but keep some spacing for readability
		if cleanedLine == "" {
			if !lastLineEmpty {
				cleanedLines = append(cleanedLines, "")
				lastLineEmpty = true
			}
		} else {
			cleanedLines = append(cleanedLines, cleanedLine)
			lastLineEmpty = false
		}
	}

	// Remove trailing empty lines
	for len(cleanedLines) > 0 && cleanedLines[len(cleanedLines)-1] == "" {
		cleanedLines = cleanedLines[:len(cleanedLines)-1]
	}

	return strings.Join(cleanedLines, "\n")
}

// cleanLine removes box characters and unnecessary formatting from a single line
func cleanLine(line string) string {
	// Remove box characters
	line = strings.ReplaceAll(line, "┌", "")
	line = strings.ReplaceAll(line, "┐", "")
	line = strings.ReplaceAll(line, "│", "")
	line = strings.ReplaceAll(line, "└", "")
	line = strings.ReplaceAll(line, "┘", "")
	line = strings.ReplaceAll(line, "─", "")
	line = strings.ReplaceAll(line, "━", "")
	line = strings.ReplaceAll(line, "┃", "")
	line = strings.ReplaceAll(line, "┏", "")
	line = strings.ReplaceAll(line, "┓", "")
	line = strings.ReplaceAll(line, "┗", "")
	line = strings.ReplaceAll(line, "┛", "")

	// Remove excessive whitespace
	line = strings.TrimSpace(line)

	// Skip empty lines or lines with only formatting
	if line == "" || strings.TrimSpace(line) == "" {
		return ""
	}

	// Clean up common formatting issues
	line = strings.ReplaceAll(line, "  ", " ")  // Replace double spaces with single spaces
	line = strings.ReplaceAll(line, "   ", " ") // Replace triple spaces with single spaces

	// Remove lines that are just formatting artifacts
	if strings.TrimSpace(line) == "" ||
		strings.TrimSpace(line) == " " ||
		strings.TrimSpace(line) == "  " {
		return ""
	}

	return line
}

// showHeader displays the Adhar Platform header with ASCII art
func showHeader() {
	// ASCII Art for ADHAR Platform
	const adharArt = `
    _      ____    _    _      _      ____   
   / \    |  _ \  | |  | |    / \    |  _ \  
  / _ \   | | | | | |__| |   / _ \   | |_) | 
 / ___ \  | |_| | |  __  |  / ___ \  |  _ <  
/_/   \_\ |____/  |_|  |_| /_/   \_\ |_| \_\ `

	// Print ASCII art with styling
	fmt.Println(helpers.TitleStyle.Render(adharArt))
	fmt.Println(helpers.SubtitleStyle.Render(" Platform " + globals.Version + " - The Open Foundation"))
	fmt.Println() // Add a blank line for spacing
}

// showFooter displays the Adhar Platform footer
func showFooter() {
	fmt.Println() // Add a blank line for spacing
	fmt.Println(helpers.SubtitleStyle.Render("Adhar • Built with ❤️ for developers!"))
	fmt.Println() // Add a blank line for spacing
}

// showPlatformOverview displays detailed information about the Adhar platform
func showPlatformOverview() {
	fmt.Println(helpers.HeaderStyle.Render("🏗️ Platform Overview"))
	fmt.Println()
	fmt.Println(helpers.InfoStyle.Render("Adhar Platform is designed to streamline your software development lifecycle"))
	fmt.Println(helpers.InfoStyle.Render("with a comprehensive Internal Developer Platform built on Kubernetes and GitOps principles."))
	fmt.Println()

	fmt.Println(helpers.TitleStyle.Render("Core Capabilities:"))
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " " + helpers.InfoStyle.Render("Unified Kubernetes-native approach for the entire development journey"))
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " " + helpers.InfoStyle.Render("GitOps-driven deployment and configuration management"))
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " " + helpers.InfoStyle.Render("Built-in security, compliance, and governance features"))
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " " + helpers.InfoStyle.Render("Comprehensive monitoring, logging, and observability"))
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " " + helpers.InfoStyle.Render("Multi-cloud and hybrid deployment support"))
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " " + helpers.InfoStyle.Render("Developer-friendly tools and automation"))
	fmt.Println()

	fmt.Println(helpers.TitleStyle.Render("Key Benefits:"))
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " " + helpers.InfoStyle.Render("Accelerate development cycles with pre-configured tooling"))
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " " + helpers.InfoStyle.Render("Reduce operational overhead through automation"))
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " " + helpers.InfoStyle.Render("Ensure consistency across development environments"))
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " " + helpers.InfoStyle.Render("Built-in security best practices and compliance"))
	fmt.Println("  " + helpers.BulletStyle.Render("•") + " " + helpers.InfoStyle.Render("Scalable architecture for teams of any size"))
	fmt.Println()
}
