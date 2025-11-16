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

package service

import (
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
)

// ServiceCmd represents the service command
var ServiceCmd = &cobra.Command{
	Use:   "service",
	Short: "Manage Kubernetes services and API endpoints",
	Long: `Manage Kubernetes services, load balancing, and API endpoints for the Adhar platform.
	
This command provides:
• Service discovery and load balancing
• API endpoint management
• Service mesh configuration
• Traffic routing and splitting
• Service health monitoring
• API documentation and testing

Examples:
  adhar service list                    # List all services
  adhar service create --name=api      # Create new service
  adhar service route --name=api       # Configure routing
  adhar service test --name=api        # Test service connectivity`,
	RunE: runService,
}

var (
	// Service command flags
	serviceName string
	serviceType string
	namespace   string
	port        string
	targetPort  string
	timeout     string
	output      string
	detailed    bool
)

func init() {
	// Service command flags
	ServiceCmd.Flags().StringVarP(&serviceName, "name", "n", "", "Service name")
	ServiceCmd.Flags().StringVarP(&serviceType, "type", "t", "", "Service type (ClusterIP, NodePort, LoadBalancer)")
	ServiceCmd.Flags().StringVarP(&namespace, "namespace", "s", "", "Namespace")
	ServiceCmd.Flags().StringVarP(&port, "port", "p", "", "Service port")
	ServiceCmd.Flags().StringVarP(&targetPort, "target-port", "r", "", "Target port")
	ServiceCmd.Flags().StringVarP(&timeout, "timeout", "i", "30s", "Operation timeout")
	ServiceCmd.Flags().StringVarP(&output, "output", "f", "", "Output format (table, json, yaml)")
	ServiceCmd.Flags().BoolVarP(&detailed, "detailed", "d", false, "Show detailed information")

	// Add subcommands
	ServiceCmd.AddCommand(listCmd)
	ServiceCmd.AddCommand(createCmd)
	ServiceCmd.AddCommand(routeCmd)
	ServiceCmd.AddCommand(testCmd)
	ServiceCmd.AddCommand(monitorCmd)
}

func runService(cmd *cobra.Command, args []string) error {
	logger.Info("🌐 Service management - use subcommands for specific service tasks")
	logger.Info("Available subcommands:")
	logger.Info("  list    - List all services")
	logger.Info("  create  - Create new services")
	logger.Info("  route   - Configure service routing")
	logger.Info("  test    - Test service connectivity")
	logger.Info("  monitor - Monitor service health")

	return cmd.Help()
}
