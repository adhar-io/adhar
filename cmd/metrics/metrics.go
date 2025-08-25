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

package metrics

import (
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
)

// MetricsCmd represents the metrics command
var MetricsCmd = &cobra.Command{
	Use:   "metrics",
	Short: "Manage metrics and monitoring",
	Long: `Manage metrics, monitoring, and alerting for the Adhar platform.
	
This command provides:
• Prometheus metrics collection and management
• Custom metrics creation and configuration
• Alerting rules and notification management
• Metrics visualization and dashboards
• Performance monitoring and analysis
• Resource utilization tracking

Examples:
  adhar metrics list                    # List all metrics
  adhar metrics create --name=cpu_usage # Create custom metric
  adhar metrics alert --rule=high_cpu   # Configure alerting
  adhar metrics dashboard --name=main   # Manage dashboards`,
	RunE: runMetrics,
}

var (
	// Metrics command flags
	metricName string
	metricType string
	namespace  string
	service    string
	timeout    string
	output     string
	detailed   bool
)

func init() {
	// Metrics command flags
	MetricsCmd.Flags().StringVarP(&metricName, "name", "n", "", "Metric name")
	MetricsCmd.Flags().StringVarP(&metricType, "type", "t", "", "Metric type (counter, gauge, histogram, summary)")
	MetricsCmd.Flags().StringVarP(&namespace, "namespace", "s", "", "Namespace")
	MetricsCmd.Flags().StringVarP(&service, "service", "e", "", "Service name")
	MetricsCmd.Flags().StringVarP(&timeout, "timeout", "i", "30s", "Operation timeout")
	MetricsCmd.Flags().StringVarP(&output, "output", "f", "", "Output format (table, json, yaml)")
	MetricsCmd.Flags().BoolVarP(&detailed, "detailed", "d", false, "Show detailed information")

	// Add subcommands
	MetricsCmd.AddCommand(listCmd)
	MetricsCmd.AddCommand(createCmd)
	MetricsCmd.AddCommand(alertCmd)
	MetricsCmd.AddCommand(dashboardCmd)
	MetricsCmd.AddCommand(exportCmd)
}

func runMetrics(cmd *cobra.Command, args []string) error {
	logger.Info("📊 Metrics management - use subcommands for specific metrics tasks")
	logger.Info("Available subcommands:")
	logger.Info("  list      - List all metrics")
	logger.Info("  create    - Create custom metrics")
	logger.Info("  alert     - Manage alerting rules")
	logger.Info("  dashboard - Manage dashboards")
	logger.Info("  export    - Export metrics data")

	return cmd.Help()
}
