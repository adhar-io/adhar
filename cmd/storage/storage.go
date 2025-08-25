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

package storage

import (
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
)

// StorageCmd represents the storage command
var StorageCmd = &cobra.Command{
	Use:   "storage",
	Short: "Manage storage and volumes",
	Long: `Manage storage, volumes, and persistent data for the Adhar platform.
	
This command provides:
• Persistent volume management
• Storage class configuration
• Volume provisioning and deprovisioning
• Storage capacity monitoring
• Backup and snapshot management
• Storage performance optimization

Examples:
  adhar storage list                    # List all volumes
  adhar storage create --name=data      # Create new volume
  adhar storage snapshot --name=data    # Create volume snapshot
  adhar storage monitor --name=data     # Monitor storage usage`,
	RunE: runStorage,
}

var (
	// Storage command flags
	volumeName   string
	storageClass string
	namespace    string
	size         string
	timeout      string
	output       string
	detailed     bool
)

func init() {
	// Storage command flags
	StorageCmd.Flags().StringVarP(&volumeName, "name", "n", "", "Volume name")
	StorageCmd.Flags().StringVarP(&storageClass, "class", "c", "", "Storage class")
	StorageCmd.Flags().StringVarP(&namespace, "namespace", "s", "", "Namespace")
	StorageCmd.Flags().StringVarP(&size, "size", "z", "", "Volume size (e.g., 10Gi)")
	StorageCmd.Flags().StringVarP(&timeout, "timeout", "i", "5m", "Operation timeout")
	StorageCmd.Flags().StringVarP(&output, "output", "f", "", "Output format (table, json, yaml)")
	StorageCmd.Flags().BoolVarP(&detailed, "detailed", "d", false, "Show detailed information")

	// Add subcommands
	StorageCmd.AddCommand(listCmd)
	StorageCmd.AddCommand(createCmd)
	StorageCmd.AddCommand(snapshotCmd)
	StorageCmd.AddCommand(monitorCmd)
	StorageCmd.AddCommand(optimizeCmd)
}

func runStorage(cmd *cobra.Command, args []string) error {
	logger.Info("💾 Storage management - use subcommands for specific storage tasks")
	logger.Info("Available subcommands:")
	logger.Info("  list     - List all volumes")
	logger.Info("  create   - Create new volumes")
	logger.Info("  snapshot - Manage volume snapshots")
	logger.Info("  monitor  - Monitor storage usage")
	logger.Info("  optimize - Optimize storage performance")

	return cmd.Help()
}
