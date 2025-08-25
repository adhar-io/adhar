package config

import (
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
)

var exportCmd = &cobra.Command{
	Use:   "export [config-name]",
	Short: "Export configuration",
	Args:  cobra.ExactArgs(1),
	RunE:  runExport,
}

func runExport(cmd *cobra.Command, args []string) error {
	configName := args[0]
	logger.Info("📤 Exporting configuration: " + configName)
	// TODO: Implement configuration export
	return nil
}
