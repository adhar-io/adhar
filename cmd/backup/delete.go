package backup

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

var (
	deleteCmd = &cobra.Command{
		Use:   "delete [backup-name]",
		Short: "Delete a backup",
		Long:  "Delete a specific backup or multiple backups matching patterns",
		Args:  cobra.ExactArgs(1),
		RunE:  runDeleteBackup,
	}

	// Delete-specific flags
	forceDelete bool
	pattern     string
)

func init() {
	deleteCmd.Flags().BoolVarP(&forceDelete, "force", "f", false, "Force deletion without confirmation")
	deleteCmd.Flags().StringVarP(&pattern, "pattern", "p", "", "Delete backups matching pattern")
}

func runDeleteBackup(cmd *cobra.Command, args []string) error {
	backupName := args[0]

	if pattern != "" {
		return deleteBackupsByPattern(pattern)
	}

	return deleteSingleBackup(backupName)
}

func deleteSingleBackup(backupName string) error {
	backupPath := filepath.Join(backupDir, backupName)

	// Check if backup exists
	if _, err := os.Stat(backupPath); os.IsNotExist(err) {
		return fmt.Errorf("backup not found: %s", backupName)
	}

	// Confirm deletion unless forced
	if !forceDelete {
		fmt.Printf("🗑️  Are you sure you want to delete backup: %s? (y/N): ", backupName)
		var response string
		fmt.Scanln(&response)
		if response != "y" && response != "Y" {
			fmt.Println("❌ Deletion cancelled")
			return nil
		}
	}

	// Delete the backup
	if err := os.Remove(backupPath); err != nil {
		return fmt.Errorf("failed to delete backup: %w", err)
	}

	fmt.Printf("✅ Successfully deleted backup: %s\n", backupName)
	return nil
}

func deleteBackupsByPattern(pattern string) error {
	// Check if backup directory exists
	if _, err := os.Stat(backupDir); os.IsNotExist(err) {
		return fmt.Errorf("backup directory not found: %s", backupDir)
	}

	// Find matching backups
	var matchingBackups []string
	err := filepath.Walk(backupDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if !info.IsDir() {
			if matched, err := filepath.Match(pattern, info.Name()); err == nil && matched {
				matchingBackups = append(matchingBackups, info.Name())
			}
		}

		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to search backups: %w", err)
	}

	if len(matchingBackups) == 0 {
		fmt.Printf("📭 No backups found matching pattern: %s\n", pattern)
		return nil
	}

	// Show matching backups
	fmt.Printf("🔍 Found %d backups matching pattern '%s':\n", len(matchingBackups), pattern)
	for _, backup := range matchingBackups {
		fmt.Printf("  - %s\n", backup)
	}

	// Confirm deletion unless forced
	if !forceDelete {
		fmt.Printf("\n🗑️  Are you sure you want to delete these %d backups? (y/N): ", len(matchingBackups))
		var response string
		fmt.Scanln(&response)
		if response != "y" && response != "Y" {
			fmt.Println("❌ Deletion cancelled")
			return nil
		}
	}

	// Delete matching backups
	deletedCount := 0
	for _, backup := range matchingBackups {
		backupPath := filepath.Join(backupDir, backup)
		if err := os.Remove(backupPath); err != nil {
			fmt.Printf("⚠️  Failed to delete %s: %v\n", backup, err)
		} else {
			deletedCount++
		}
	}

	fmt.Printf("✅ Successfully deleted %d backups\n", deletedCount)
	return nil
}
