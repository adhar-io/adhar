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

// EnvironmentCmd represents the environment command
var EnvironmentCmd = &cobra.Command{
	Use: "environment",
	// The short forms stay as aliases so existing scripts and muscle memory
	// keep working after the rename.
	Aliases: []string{"env", "envs"},
	Short:   "Manage platform environments",
	Long: `Manage different environments for the Adhar platform.
	
This command provides:
• Environment creation and configuration
• Environment switching and context management
• Environment backup and restoration
• Environment comparison and validation
• Multi-environment operations

Examples:
  adhar environment create dev              # Create development environment
  adhar environment create staging          # Create staging environment
  adhar environment create prod             # Create production environment
  adhar environment list                    # List all environments
  adhar environment switch dev              # Switch context to dev environment
  adhar environment delete staging          # Delete staging environment
  adhar environment backup prod             # Backup production environment
  adhar environment restore prod backup-2024-01-01 # Restore from backup`,
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
	EnvironmentCmd.PersistentFlags().StringVarP(&environment, "environment", "e", "", "Environment name")
	EnvironmentCmd.PersistentFlags().StringVarP(&provider, "provider", "p", "", "Cloud provider for environment")
	EnvironmentCmd.PersistentFlags().StringVarP(&region, "region", "r", "", "Cloud region for environment")
	EnvironmentCmd.PersistentFlags().StringVarP(&config, "config", "c", "", "Environment configuration file")

	// Add subcommands
	EnvironmentCmd.AddCommand(createCmd)
	EnvironmentCmd.AddCommand(listCmd)
	EnvironmentCmd.AddCommand(switchCmd)
	EnvironmentCmd.AddCommand(deleteCmd)
	EnvironmentCmd.AddCommand(backupCmd)
	EnvironmentCmd.AddCommand(restoreCmd)
	EnvironmentCmd.AddCommand(configCmd)
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
