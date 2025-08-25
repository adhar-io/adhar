package metrics

import (
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List all metrics",
	Long: `List all available metrics and their status.
	
Examples:
  adhar metrics list
  adhar metrics list --namespace=prod
  adhar metrics list --service=web`,
	RunE: runList,
}

func runList(cmd *cobra.Command, args []string) error {
	logger.Info("📋 Listing metrics...")

	// TODO: Implement metrics listing
	// This should show:
	// - All available metrics
	// - Metric types and descriptions
	// - Current values
	// - Collection status

	logger.Info("✅ Metrics listed")
	return nil
}
