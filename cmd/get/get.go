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

package get

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

// GetCmd represents the get command
var GetCmd = &cobra.Command{
	Use:   "get",
	Short: "Get information about Adhar platform resources",
	Long: `Get information about various Adhar platform resources.
	
This command provides access to:
• Platform secrets and credentials
• Application status and information
• Cluster health and status
• Environment configurations
• Resource usage and metrics

Examples:
  adhar get secrets                    # Get all platform secrets
  adhar get secrets -p argocd         # Get ArgoCD specific secrets
  adhar get applications              # List all applications
  adhar get status                    # Get platform status
  adhar get clusters                  # List all clusters`,
	RunE: runGet,
}

var (
	// Global flags for get command
	outputFormat  string
	namespace     string
	allNamespaces bool
	verbose       bool
)

func init() {
	// Global flags
	GetCmd.PersistentFlags().StringVarP(&outputFormat, "output", "o", "table", "Output format (table, json, yaml)")
	GetCmd.PersistentFlags().StringVarP(&namespace, "namespace", "n", "", "Namespace to query")
	GetCmd.PersistentFlags().BoolVarP(&allNamespaces, "all-namespaces", "A", false, "Query all namespaces")
	// Verbose flag is handled globally by root command

	// Add subcommands
	GetCmd.AddCommand(secretsCmd)
	GetCmd.AddCommand(clusterCmd)
	// TODO: These will be implemented in separate files
	// GetCmd.AddCommand(applicationsCmd)
	// GetCmd.AddCommand(statusCmd)
}

func runGet(cmd *cobra.Command, args []string) error {
	// Enhanced display for the get command
	fmt.Println("🔍 Get command - use subcommands to get specific information")
	fmt.Println()

	// Create a bordered box for available resource types
	resourceTypes := []string{
		"🔐 secrets      - Platform secrets and credentials",
		"🚀 applications - Application lifecycle management",
		"📊 status       - Platform health and status",
		"🏗️  clusters     - Cluster information and status",
		"🌍 environments - Environment configurations",
		"💾 databases    - Database instances and status",
		"🛠️  managedtools - Platform tools and services",
		"🛣️  routes       - Network routes and ingress",
	}

	var resourcesBuilder strings.Builder
	resourcesBuilder.WriteString("📋 AVAILABLE RESOURCE TYPES:\n")
	resourcesBuilder.WriteString("                         \n")

	for _, resource := range resourceTypes {
		resourcesBuilder.WriteString(fmt.Sprintf("  %s\n", resource))
	}

	// Create bordered box for resource types
	resourceBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#8b5cf6")).
		Padding(1, 2).
		Margin(1, 0).
		Width(70).
		Render(resourcesBuilder.String())

	fmt.Println(resourceBox)

	// Create bordered box for usage examples
	examples := []string{
		"adhar get secrets                    # Get all platform secrets",
		"adhar get secrets -p argocd         # Get ArgoCD specific secrets",
		"adhar get cluster                    # Get cluster information",
		"adhar get cluster --detailed        # Get detailed cluster info",
		"adhar get applications               # List all applications",
		"adhar get status                     # Get platform status",
		"adhar get environments --all-namespaces # List envs across all namespaces",
		"adhar get all                        # List all Adhar resources",
	}

	var examplesBuilder strings.Builder
	examplesBuilder.WriteString("🚀 USAGE EXAMPLES:\n")
	examplesBuilder.WriteString("                 \n")

	for _, example := range examples {
		examplesBuilder.WriteString(fmt.Sprintf("  %s\n", example))
	}

	examplesBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#06b6d4")).
		Padding(1, 2).
		Margin(1, 0).
		Width(70).
		Render(examplesBuilder.String())

	fmt.Println(examplesBox)

	fmt.Println()
	fmt.Println("💡 Tip: Use 'adhar get <resource> --help' for detailed information about each resource type")

	return nil
}
