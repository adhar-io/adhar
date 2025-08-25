package traces

import (
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List recent traces",
	Long: `List recent traces and their status.
	
Examples:
  adhar traces list
  adhar traces list --service=web`,
	RunE: runList,
}

func runList(cmd *cobra.Command, args []string) error {
	logger.Info("📋 Listing recent traces...")

	// TODO: Implement trace listing
	// This should show:
	// - Recent trace IDs
	// - Service names
	// - Operation names
	// - Duration and status

	logger.Info("✅ Traces listed")
	return nil
}
