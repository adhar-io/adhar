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

package traces

import (
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
)

// TracesCmd represents the traces command
var TracesCmd = &cobra.Command{
	Use:   "traces",
	Short: "Manage distributed tracing",
	Long: `Manage distributed tracing and observability for the Adhar platform.
	
This command provides:
• Jaeger trace collection and analysis
• Trace sampling and configuration
• Performance analysis and optimization
• Service dependency mapping
• Latency analysis and troubleshooting
• Trace correlation and debugging

Examples:
  adhar traces list                    # List recent traces
  adhar traces search --service=web   # Search traces by service
  adhar traces analyze --trace=abc123 # Analyze specific trace
  adhar traces config --sampling=0.1  # Configure trace sampling`,
	RunE: runTraces,
}

var (
	// Traces command flags
	traceID    string
	service    string
	operation  string
	timeout    string
	output     string
	detailed   bool
	tempoURL   string
	traceLimit int
	tags       string
)

func init() {
	// Traces command flags
	TracesCmd.Flags().StringVarP(&traceID, "trace", "i", "", "Trace ID")
	TracesCmd.Flags().StringVarP(&service, "service", "e", "", "Service name")
	TracesCmd.Flags().StringVarP(&operation, "operation", "p", "", "Operation name")
	TracesCmd.Flags().StringVarP(&timeout, "timeout", "m", "30s", "Operation timeout")
	TracesCmd.Flags().StringVarP(&output, "output", "f", "", "Output format (table, json, yaml)")
	TracesCmd.Flags().BoolVarP(&detailed, "detailed", "d", false, "Show detailed information")

	// Tempo endpoint and search options (shared by subcommands).
	TracesCmd.PersistentFlags().StringVar(&tempoURL, "tempo-url", defaultTempoURL, "Tempo HTTP API base URL")
	TracesCmd.PersistentFlags().IntVarP(&traceLimit, "limit", "l", 20, "Maximum number of traces to return")
	TracesCmd.PersistentFlags().StringVar(&tags, "tags", "", "Additional TraceQL tag filters (e.g. 'http.status_code=500')")

	// Add subcommands
	TracesCmd.AddCommand(listCmd)
	TracesCmd.AddCommand(searchCmd)
	TracesCmd.AddCommand(analyzeCmd)
	TracesCmd.AddCommand(configCmd)
	TracesCmd.AddCommand(exportCmd)
}

func runTraces(cmd *cobra.Command, args []string) error {
	logger.Info("🔍 Traces management - use subcommands for specific tracing tasks")
	logger.Info("Available subcommands:")
	logger.Info("  list     - List recent traces")
	logger.Info("  search   - Search traces")
	logger.Info("  analyze  - Analyze trace performance")
	logger.Info("  config   - Configure tracing")
	logger.Info("  export   - Export trace data")

	return cmd.Help()
}
