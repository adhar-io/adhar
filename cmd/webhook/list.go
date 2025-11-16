package webhook

import (
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List all webhooks",
	Long: `List all configured webhooks and their status.
	
Examples:
  adhar webhook list
  adhar webhook list --type=github`,
	RunE: runList,
}

func runList(cmd *cobra.Command, args []string) error {
	logger.Info("📋 Listing webhooks...")

	// TODO: Implement webhook listing
	// This should show:
	// - All webhook names
	// - Webhook types and URLs
	// - Current status
	// - Last activity time

	logger.Info("✅ Webhooks listed")
	return nil
}
