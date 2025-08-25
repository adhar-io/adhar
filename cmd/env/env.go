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

package env

import (
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
)

// EnvCmd represents the environment command
var EnvCmd = &cobra.Command{
	Use:   "env",
	Short: "Manage platform environments",
	Long: `Manage different environments for the Adhar platform.
	
This command provides:
• Environment creation and configuration
• Environment switching and context management
• Environment backup and restoration
• Environment comparison and validation
• Multi-environment operations

Examples:
  adhar env create dev              # Create development environment
  adhar env create staging          # Create staging environment
  adhar env create prod             # Create production environment
  adhar env list                    # List all environments
  adhar env switch dev              # Switch context to dev environment
  adhar env delete staging          # Delete staging environment
  adhar env backup prod             # Backup production environment
  adhar env restore prod backup-2024-01-01 # Restore from backup`,
	RunE: runEnv,
}

var (
	// Environment command flags
	environment string
	provider    string
	region      string
	config      string
)

func init() {
	// Environment command flags
	EnvCmd.PersistentFlags().StringVarP(&environment, "environment", "e", "", "Environment name")
	EnvCmd.PersistentFlags().StringVarP(&provider, "provider", "p", "", "Cloud provider for environment")
	EnvCmd.PersistentFlags().StringVarP(&region, "region", "r", "", "Cloud region for environment")
	EnvCmd.PersistentFlags().StringVarP(&config, "config", "c", "", "Environment configuration file")

	// Add subcommands
	EnvCmd.AddCommand(createCmd)
	EnvCmd.AddCommand(listCmd)
	EnvCmd.AddCommand(switchCmd)
	EnvCmd.AddCommand(deleteCmd)
	EnvCmd.AddCommand(backupCmd)
	EnvCmd.AddCommand(restoreCmd)
	EnvCmd.AddCommand(configCmd)
}

func runEnv(cmd *cobra.Command, args []string) error {
	logger.Info("🌍 Environment management - use subcommands to manage environments")
	logger.Info("Available subcommands:")
	logger.Info("  create  - Create new environment")
	logger.Info("  list    - List all environments")
	logger.Info("  switch  - Switch to environment")
	logger.Info("  delete  - Delete environment")
	logger.Info("  backup  - Backup environment")
	logger.Info("  restore - Restore environment")
	logger.Info("  config  - Manage environment configuration")

	return cmd.Help()
}
