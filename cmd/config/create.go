package config

import (
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
)

var createCmd = &cobra.Command{
	Use:   "create",
	Short: "Create new configuration files",
	RunE:  runCreate,
}

var (
	createProvider string
	createRegion   string
	createName     string
)

func init() {
	createCmd.Flags().StringVarP(&createProvider, "provider", "p", "", "Cloud provider")
	createCmd.Flags().StringVarP(&createRegion, "region", "r", "", "Cloud region")
	createCmd.Flags().StringVarP(&createName, "name", "n", "", "Configuration name")
}

func runCreate(cmd *cobra.Command, args []string) error {
	logger.Info("📝 Creating new configuration...")
	// TODO: Implement configuration creation
	return nil
}
