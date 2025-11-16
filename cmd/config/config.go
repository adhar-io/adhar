/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the file at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package config

import (
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
)

// ConfigCmd represents the config command
var ConfigCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage Adhar platform configuration",
	Long: `Manage Adhar platform configuration files and settings.
	
This command provides:
• Configuration file creation and validation
• Environment template management
• Provider configuration management
• Configuration validation and testing
• Configuration export and import

Examples:
  adhar config create --provider=gke --region=us-central1
  adhar config validate config.yaml
  adhar config list-templates`,
	RunE: runConfig,
}

var (
	// Global flags for config command
	configFile string
)

func init() {
	// Global flags
	ConfigCmd.PersistentFlags().StringVarP(&configFile, "file", "f", "", "Configuration file path")
	// Verbose flag is handled globally by root command

	// Add subcommands
	ConfigCmd.AddCommand(createCmd)
	ConfigCmd.AddCommand(validateCmd)
	ConfigCmd.AddCommand(listTemplatesCmd)
	ConfigCmd.AddCommand(exportCmd)
}

func runConfig(cmd *cobra.Command, args []string) error {
	logger.Info("⚙️ Config command - use subcommands to manage configuration")
	logger.Info("Available subcommands:")
	logger.Info("  create        - Create new configuration files")
	logger.Info("  validate      - Validate configuration files")
	logger.Info("  list-templates - List available configuration templates")
	logger.Info("  export        - Export configuration")

	return cmd.Help()
}
