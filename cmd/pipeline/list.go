package pipeline

import (
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List all pipelines",
	Long: `List all available pipelines and their status.
	
Examples:
  adhar pipeline list
  adhar pipeline list --type=deploy`,
	RunE: runList,
}

func runList(cmd *cobra.Command, args []string) error {
	logger.Info("📋 Listing pipelines...")

	// TODO: Implement pipeline listing
	// This should show:
	// - All pipeline names
	// - Pipeline types
	// - Current status
	// - Last run time

	logger.Info("✅ Pipelines listed")
	return nil
}
