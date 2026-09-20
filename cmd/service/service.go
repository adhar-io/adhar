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

// RoutesCmd represents the routes command.
//
// Named `routes` rather than `service`: "service" means two unrelated things on a
// platform like this — a Kubernetes Service, and a thing a team owns and ships —
// and the ambiguity showed up in the help text, where endpoint inspection sat next
// to a supply-chain scaffold. `routes` says which one this is. The old name stays
// as an alias so existing scripts and bookmarks keep working.
var RoutesCmd = &cobra.Command{
	Use:     "routes",
	Aliases: []string{"service", "svc", "route"},
	Short:   "Inspect and manage Services, endpoints and ingress routes",
	Long: `Inspect and manage how traffic reaches your workloads.

This is the network edge of an application: the Kubernetes Service in front of it,
the endpoints behind that Service, and the HTTPRoute that publishes it on the
platform Gateway. It answers "is this reachable, and by what name".

It provides:
• Service discovery and load balancing
• Endpoint readiness — whether a Service actually has backends
• Ingress routes and traffic splitting
• Connectivity testing from inside the cluster

Examples:
  adhar routes list                    # List all Services
  adhar routes create --name=api       # Create a Service
  adhar routes show --name=api         # Show the ingress routes for a Service
  adhar routes monitor                 # Endpoint readiness across the namespace
  adhar routes test --name=api         # Test connectivity to a Service

To take an application from source to running, use 'adhar push' — that is the
supply chain, not a routing concern.`,
	RunE: runService,
}

// ServiceCmd is the previous name, kept so callers that reference the exported
// symbol continue to compile.
var ServiceCmd = RoutesCmd

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
	ServiceCmd.AddCommand(newServiceCmd)
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
