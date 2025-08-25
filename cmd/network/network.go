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

package network

import (
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
)

// NetworkCmd represents the network command
var NetworkCmd = &cobra.Command{
	Use:   "network",
	Short: "Network diagnostics and management",
	Long: `Perform network diagnostics and manage network policies for the Adhar platform.
	
This command provides:
• Service mesh diagnostics and troubleshooting
• Network policy management and validation
• Connectivity testing and monitoring
• Traffic flow analysis and visualization
• Network security policy enforcement
• Load balancer and ingress diagnostics

Examples:
  adhar network diagnose                    # Run network diagnostics
  adhar network diagnose --service=web     # Diagnose specific service
  adhar network policy list                # List network policies
  adhar network policy apply --file=policy.yaml # Apply network policy`,
	RunE: runNetwork,
}

var (
	// Network command flags
	service   string
	namespace string
	policy    string
	timeout   string
	output    string
	detailed  bool
)

func init() {
	// Network command flags
	NetworkCmd.Flags().StringVarP(&service, "service", "s", "", "Service name")
	NetworkCmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Namespace")
	NetworkCmd.Flags().StringVarP(&policy, "policy", "p", "", "Policy name or file")
	NetworkCmd.Flags().StringVarP(&timeout, "timeout", "i", "30s", "Operation timeout")
	NetworkCmd.Flags().StringVarP(&output, "output", "f", "", "Output format (table, json, yaml)")
	NetworkCmd.Flags().BoolVarP(&detailed, "detailed", "d", false, "Show detailed information")

	// Add subcommands
	NetworkCmd.AddCommand(diagnoseCmd)
	NetworkCmd.AddCommand(policyCmd)
	NetworkCmd.AddCommand(connectivityCmd)
	NetworkCmd.AddCommand(trafficCmd)
}

func runNetwork(cmd *cobra.Command, args []string) error {
	logger.Info("🌐 Network management - use subcommands for specific network tasks")
	logger.Info("Available subcommands:")
	logger.Info("  diagnose    - Run network diagnostics")
	logger.Info("  policy      - Manage network policies")
	logger.Info("  connectivity - Test network connectivity")
	logger.Info("  traffic     - Analyze network traffic")

	return cmd.Help()
}
