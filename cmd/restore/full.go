package restore

import (
	"fmt"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
)

var (
	fullCmd = &cobra.Command{
		Use:   "full [backup-name]",
		Short: "Full platform restoration",
		Long: `Perform a complete restoration of the Adhar platform from a Velero backup.
Creates a velero.io/v1 Restore covering all namespaces and restoring persistent
volumes. The source backup is given as an argument, via --from-backup, or the
global --backup flag.

Examples:
  adhar restore full my-backup
  adhar restore full --from-backup=prod-nightly
  adhar restore full my-backup --name=dr-restore`,
		Args: cobra.MaximumNArgs(1),
		RunE: runFullRestore,
	}

	// Full restore specific flags
	fullFromBackup  string
	fullRestoreName string
)

func init() {
	fullCmd.Flags().StringVar(&fullFromBackup, "from-backup", "", "Name of the Velero backup to restore from")
	fullCmd.Flags().StringVar(&fullRestoreName, "name", "", "Name for the Restore object (default: <backup>-restore-<timestamp>)")
}

func runFullRestore(cmd *cobra.Command, args []string) error {
	src := resolveBackupName(args, fullFromBackup)
	if src == "" {
		return fmt.Errorf("a source backup is required (argument, --from-backup, or --backup)")
	}

	name := fullRestoreName
	if name == "" {
		name = defaultRestoreName(src)
	}

	fmt.Printf("🔄 Full platform restore %q from backup %q\n", name, src)

	spec := map[string]interface{}{
		"backupName":         src,
		"includedNamespaces": []interface{}{"*"},
		"restorePVs":         true,
	}

	if dryRun {
		fmt.Println(helpers.CreateMuted("   DRY RUN - no Restore object created"))
		fmt.Println(helpers.CreateMuted("   Would restore: all namespaces, persistent volumes included"))
		return nil
	}

	return createVeleroRestore(name, spec)
}

// resolveBackupName picks the source backup from (in order) a positional arg,
// a command-specific --from-backup flag, or the global --backup flag.
func resolveBackupName(args []string, fromBackupFlag string) string {
	if len(args) > 0 && args[0] != "" {
		return args[0]
	}
	if fromBackupFlag != "" {
		return fromBackupFlag
	}
	return backupPath
}
