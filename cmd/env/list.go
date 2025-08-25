package env

import (
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List all environments",
	RunE:  runList,
}

func runList(cmd *cobra.Command, args []string) error {
	logger.Info("📋 Listing all environments...")

	// TODO: Implement environment listing
	// This should show:
	// - Environment names
	// - Status (active, inactive, error)
	// - Provider and region
	// - Creation date
	// - Resource usage

	logger.Info("✅ Environment list displayed")
	return nil
}
