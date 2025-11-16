package config

import (
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
)

var listTemplatesCmd = &cobra.Command{
	Use:   "list-templates",
	Short: "List available configuration templates",
	RunE:  runListTemplates,
}

func runListTemplates(cmd *cobra.Command, args []string) error {
	logger.Info("📋 Listing available configuration templates...")
	// TODO: Implement template listing
	return nil
}
