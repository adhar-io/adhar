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

package security

import (
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
)

// SecurityCmd represents the security command
var SecurityCmd = &cobra.Command{
	Use:   "security",
	Short: "Security scanning and management",
	Long: `Perform security operations on the Adhar platform.
	
This command provides:
• Container image security scanning
• Kubernetes cluster security assessment
• Vulnerability detection and reporting
• Security policy enforcement
• Security compliance checks
• Incident response tools

Examples:
  adhar security scan                    # Run comprehensive security scan
  adhar security scan --image=nginx:latest # Scan specific image
  adhar security scan --namespace=prod   # Scan production namespace
  adhar security scan --policy=strict    # Use strict security policy
  adhar security scan --auto-fix         # Auto-fix security issues`,
	RunE: runSecurity,
}

var (
	// Shared flags — persistent so every subcommand inherits them.
	namespace string
	output    string
	severity  string
)

func init() {
	// Persistent flags shared by all security subcommands.
	SecurityCmd.PersistentFlags().StringVarP(&namespace, "namespace", "n", "", "Limit to a namespace (default: all namespaces)")
	SecurityCmd.PersistentFlags().StringVarP(&output, "output", "o", "table", "Output format: table, json, yaml")
	SecurityCmd.PersistentFlags().StringVarP(&severity, "severity", "s", "", "Minimum severity level (low, medium, high, critical)")

	// Add subcommands
	SecurityCmd.AddCommand(scanCmd)
	SecurityCmd.AddCommand(vulnerabilitiesCmd)
	SecurityCmd.AddCommand(policiesCmd)
	SecurityCmd.AddCommand(incidentsCmd)
}

func runSecurity(cmd *cobra.Command, args []string) error {
	logger.Info("🛡️ Security operations - use subcommands for specific security tasks")
	logger.Info("Available subcommands:")
	logger.Info("  scan            - Run security scans")
	logger.Info("  vulnerabilities - Manage vulnerabilities")
	logger.Info("  policies        - Manage security policies")
	logger.Info("  incidents       - Handle security incidents")

	return cmd.Help()
}
