package backup

import (
	"adhar-io/adhar/cmd/helpers"
	"fmt"

	"github.com/spf13/cobra"
)

var (
	// BackupCmd is the main backup command
	BackupCmd = &cobra.Command{
		Use:   "backup",
		Short: "Manage platform backups",
		Long: `Manage backups of the Adhar platform via Velero (velero.io/v1).

Backups are first-class Velero objects reconciled by the control plane — the
same backend the Adhar Console uses. This covers Kubernetes resources,
persistent volumes and (where configured) volume snapshots:
- Application data and configurations
- Database backups (PostgreSQL, Redis, etc.)
- Persistent volumes and storage
- Configuration and secrets
- Git repositories and ArgoCD applications`,
		RunE: runBackup,
	}
)

func init() {
	// Add subcommands
	BackupCmd.AddCommand(createCmd)
	BackupCmd.AddCommand(listCmd)
	BackupCmd.AddCommand(deleteCmd)
	BackupCmd.AddCommand(verifyCmd)
	BackupCmd.AddCommand(scheduleCmd)
}

func runBackup(cmd *cobra.Command, args []string) error {
	helpers.Println("⛨ Adhar Platform Backup Management")
	fmt.Println("")
	helpers.Println("Available commands:")
	fmt.Println("  create    - Create a new backup")
	fmt.Println("  list      - List existing backups")
	fmt.Println("  delete    - Delete a backup")
	fmt.Println("  verify    - Verify backup integrity")
	fmt.Println("  schedule  - Manage backup schedules")
	fmt.Println("")
	helpers.Println("Use 'adhar backup <command> --help' for more information")
	return nil
}
