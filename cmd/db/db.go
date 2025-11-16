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

package db

import (
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
)

// DBCmd represents the db command
var DBCmd = &cobra.Command{
	Use:   "db",
	Short: "Database management and operations",
	Long: `Manage databases and perform database operations on the Adhar platform.
	
This command provides:
• Database creation and configuration
• Backup and restore operations
• Database health monitoring
• Performance optimization
• Schema management and migrations
• Connection testing and diagnostics

Examples:
  adhar db list                    # List all databases
  adhar db create --name=myapp     # Create new database
  adhar db backup --name=myapp     # Backup database
  adhar db restore --name=myapp    # Restore database
  adhar db health --name=myapp     # Check database health`,
	RunE: runDB,
}

var (
	// Database command flags
	dbName     string
	dbType     string
	dbHost     string
	dbPort     string
	dbUser     string
	dbPassword string
	backup     bool
	restore    bool
	health     bool
)

func init() {
	// Database command flags
	DBCmd.Flags().StringVarP(&dbName, "name", "n", "", "Database name")
	DBCmd.Flags().StringVarP(&dbType, "type", "t", "", "Database type (postgresql, mysql, mongodb, redis)")
	DBCmd.Flags().StringVarP(&dbHost, "host", "", "", "Database host")
	DBCmd.Flags().StringVarP(&dbPort, "port", "", "", "Database port")
	DBCmd.Flags().StringVarP(&dbUser, "user", "u", "", "Database user")
	DBCmd.Flags().StringVarP(&dbPassword, "password", "p", "", "Database password")
	DBCmd.Flags().BoolVar(&backup, "backup", false, "Perform backup operation")
	DBCmd.Flags().BoolVar(&restore, "restore", false, "Perform restore operation")
	DBCmd.Flags().BoolVar(&health, "health", false, "Check database health")

	// Add subcommands
	DBCmd.AddCommand(createCmd)
	DBCmd.AddCommand(listCmd)
	DBCmd.AddCommand(backupCmd)
	DBCmd.AddCommand(restoreCmd)
	DBCmd.AddCommand(healthCmd)
	DBCmd.AddCommand(migrateCmd)
}

func runDB(cmd *cobra.Command, args []string) error {
	logger.Info("🗄️ Database management - use subcommands for specific database tasks")
	logger.Info("Available subcommands:")
	logger.Info("  create  - Create new databases")
	logger.Info("  list    - List all databases")
	logger.Info("  backup  - Backup databases")
	logger.Info("  restore - Restore databases")
	logger.Info("  health  - Check database health")
	logger.Info("  migrate - Manage database migrations")

	return cmd.Help()
}
