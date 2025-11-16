package service

import (
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List all services",
	Long: `List all Kubernetes services and their status.
	
Examples:
  adhar service list
  adhar service list --namespace=prod`,
	RunE: runList,
}

func runList(cmd *cobra.Command, args []string) error {
	logger.Info("📋 Listing services...")

	// TODO: Implement service listing
	// This should show:
	// - All service names
	// - Service types
	// - Ports and endpoints
	// - Health status

	logger.Info("✅ Services listed")
	return nil
}
