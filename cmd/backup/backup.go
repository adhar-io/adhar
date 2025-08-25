package backup

import (
	"fmt"

	"github.com/spf13/cobra"
)

var (
	// BackupCmd is the main backup command
	BackupCmd = &cobra.Command{
		Use:   "backup",
		Short: "Manage platform backups",
		Long: `Manage comprehensive backups of the Adhar platform including:
- Application data and configurations
- Database backups (PostgreSQL, Redis, etc.)
- Persistent volumes and storage
- Configuration and secrets
- Git repositories and ArgoCD applications`,
		RunE: runBackup,
	}

	// Global flags
	backupDir      string
	includeData    bool
	includeConfig  bool
	includeSecrets bool
	compression    bool
	encryption     bool
)

func init() {
	// Global flags
	BackupCmd.PersistentFlags().StringVarP(&backupDir, "dir", "d", "./backups", "Backup directory path")
	BackupCmd.PersistentFlags().BoolVarP(&includeData, "data", "", true, "Include application data")
	BackupCmd.PersistentFlags().BoolVarP(&includeConfig, "config", "", true, "Include configurations")
	BackupCmd.PersistentFlags().BoolVarP(&includeSecrets, "secrets", "", true, "Include secrets (encrypted)")
	BackupCmd.PersistentFlags().BoolVarP(&compression, "compress", "c", true, "Enable compression")
	BackupCmd.PersistentFlags().BoolVarP(&encryption, "encrypt", "e", false, "Enable encryption")

	// Add subcommands
	BackupCmd.AddCommand(createCmd)
	BackupCmd.AddCommand(listCmd)
	BackupCmd.AddCommand(deleteCmd)
	BackupCmd.AddCommand(verifyCmd)
	BackupCmd.AddCommand(scheduleCmd)
}

func runBackup(cmd *cobra.Command, args []string) error {
	fmt.Println("🔒 Adhar Platform Backup Management")
	fmt.Println("")
	fmt.Println("Available commands:")
	fmt.Println("  create    - Create a new backup")
	fmt.Println("  list      - List existing backups")
	fmt.Println("  delete    - Delete a backup")
	fmt.Println("  verify    - Verify backup integrity")
	fmt.Println("  schedule  - Manage backup schedules")
	fmt.Println("")
	fmt.Println("Use 'adhar backup <command> --help' for more information")
	return nil
}
