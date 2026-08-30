package restore

import (
	"fmt"

	"github.com/spf13/cobra"
)

var (
	// RestoreCmd is the main restore command
	RestoreCmd = &cobra.Command{
		Use:   "restore",
		Short: "Restore platform from Velero backups",
		Long: `Restore the Adhar platform from Velero backups (velero.io/v1 Restore),
reconciled by the control plane — the same backend the Adhar Console uses:
- Full platform restoration
- Selective component restoration (namespace / label selectors)
- Database restoration
- Configuration restoration
- Application data restoration`,
		RunE: runRestore,
	}

	// Global flags. --backup names the source Velero Backup to restore from
	// (shared by all subcommands); --dry-run prints the plan without creating a
	// Velero Restore object.
	backupPath string
	dryRun     bool
)

func init() {
	// Global flags
	RestoreCmd.PersistentFlags().StringVarP(&backupPath, "backup", "b", "", "Name of the Velero Backup to restore from")
	RestoreCmd.PersistentFlags().BoolVarP(&dryRun, "dry-run", "", false, "Show what would be restored without creating a Restore")

	// Add subcommands
	RestoreCmd.AddCommand(fullCmd)
	RestoreCmd.AddCommand(selectiveCmd)
	RestoreCmd.AddCommand(databaseCmd)
	RestoreCmd.AddCommand(configCmd)
	RestoreCmd.AddCommand(verifyCmd)
}

func runRestore(cmd *cobra.Command, args []string) error {
	fmt.Println("🔄 Adhar Platform Restore Management")
	fmt.Println("")
	fmt.Println("Available commands:")
	fmt.Println("  list      - List Velero restores")
	fmt.Println("  create    - Create a Velero restore from a backup")
	fmt.Println("  status    - Show Velero restore status")
	fmt.Println("  full      - Full platform restoration")
	fmt.Println("  selective - Selective component restoration")
	fmt.Println("  database  - Database restoration only")
	fmt.Println("  config    - Configuration restoration only")
	fmt.Println("  verify    - Verify backup before restore")
	fmt.Println("")
	fmt.Println("Use 'adhar restore <command> --help' for more information")
	return nil
}
