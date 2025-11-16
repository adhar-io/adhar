package secrets

import (
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List all secrets",
	Long: `List all secrets and their metadata.
	
Examples:
  adhar secrets list
  adhar secrets list --namespace=prod`,
	RunE: runList,
}

func runList(cmd *cobra.Command, args []string) error {
	logger.Info("📋 Listing secrets...")

	// TODO: Implement secrets listing
	// This should show:
	// - All secret names
	// - Secret types
	// - Creation dates
	// - Last modified dates

	logger.Info("✅ Secrets listed")
	return nil
}
