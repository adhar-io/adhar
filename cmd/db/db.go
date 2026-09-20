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

// DatabaseCmd represents the database command
var DatabaseCmd = &cobra.Command{
	Use: "database",
	// The short forms stay as aliases so existing scripts and muscle memory
	// keep working after the rename.
	Aliases: []string{"db"},
	Short:   "Database management and operations",
	Long: `Manage databases and perform database operations on the Adhar platform.
	
This command provides:
• Database creation and configuration
• Backup and restore operations
• Database health monitoring
• Performance optimization
• Schema management and migrations
• Connection testing and diagnostics

Examples:
  adhar database list                    # List all databases
  adhar database create --name=myapp     # Create new database
  adhar database backup --name=myapp     # Backup database
  adhar database restore --name=myapp    # Restore database
  adhar database health --name=myapp     # Check database health`,
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
	dbNS       string
	dbOutput   string
)

func init() {
	// Identity flags shared across subcommands (create/delete/status/…) that all
	// operate on a named CompositeDatabase XR, so they are persistent.
	DatabaseCmd.PersistentFlags().StringVarP(&dbName, "name", "n", "", "Database name")
	DatabaseCmd.PersistentFlags().StringVarP(&dbType, "type", "t", "", "Database type (postgresql, mysql, mongodb, redis)")

	// Database command flags
	DatabaseCmd.Flags().StringVarP(&dbHost, "host", "", "", "Database host")
	DatabaseCmd.Flags().StringVarP(&dbPort, "port", "", "", "Database port")
	DatabaseCmd.Flags().StringVarP(&dbUser, "user", "u", "", "Database user")
	DatabaseCmd.Flags().StringVarP(&dbPassword, "password", "p", "", "Database password")
	DatabaseCmd.Flags().BoolVar(&backup, "backup", false, "Perform backup operation")
	DatabaseCmd.Flags().BoolVar(&restore, "restore", false, "Perform restore operation")
	DatabaseCmd.Flags().BoolVar(&health, "health", false, "Check database health")

	// Persistent flags shared across subcommands operating on CompositeDatabase XRs.
	DatabaseCmd.PersistentFlags().StringVar(&dbNS, "namespace", "default", "Namespace for the database resources")
	DatabaseCmd.PersistentFlags().StringVarP(&dbOutput, "output", "o", "table", "Output format (table, json, yaml)")

	// Add subcommands
	DatabaseCmd.AddCommand(createCmd)
	DatabaseCmd.AddCommand(listCmd)
	DatabaseCmd.AddCommand(statusCmd)
	DatabaseCmd.AddCommand(deleteCmd)
	DatabaseCmd.AddCommand(backupCmd)
	DatabaseCmd.AddCommand(restoreCmd)
	DatabaseCmd.AddCommand(healthCmd)
	DatabaseCmd.AddCommand(migrateCmd)
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
