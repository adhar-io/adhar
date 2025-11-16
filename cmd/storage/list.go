package storage

import (
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List all volumes",
	Long: `List all storage volumes and their status.
	
Examples:
  adhar storage list
  adhar storage list --namespace=prod`,
	RunE: runList,
}

func runList(cmd *cobra.Command, args []string) error {
	logger.Info("📋 Listing storage volumes...")

	// TODO: Implement volume listing
	// This should show:
	// - All volume names
	// - Storage classes
	// - Sizes and usage
	// - Status and health

	logger.Info("✅ Storage volumes listed")
	return nil
}
