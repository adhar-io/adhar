package restore

import (
	"fmt"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
)

var (
	databaseCmd = &cobra.Command{
		Use:   "database [backup-name]",
		Short: "Database restoration only",
		Long: `Restore only database workloads from a Velero backup. Creates a
velero.io/v1 Restore scoped to the database's namespace and (optionally) a
label selector, with persistent volumes restored.

For a CloudNativePG cluster (the local default engine), a namespace-scoped
Velero restore brings back the CNPG Cluster CR and its PVCs. For a
point-in-time recovery instead, use CNPG's bootstrap.recovery on a new
CompositeDatabase referencing the backup object store.

Examples:
  adhar restore database my-backup --namespace=team-a
  adhar restore database --from-backup=nightly --namespace=team-a --selector=cnpg.io/cluster=orders`,
		Args: cobra.MaximumNArgs(1),
		RunE: runDatabaseRestore,
	}

	// Database restore specific flags
	dbFromBackup  string
	dbNamespace   string
	dbSelector    string
	dbRestoreName string
)

func init() {
	databaseCmd.Flags().StringVar(&dbFromBackup, "from-backup", "", "Name of the Velero backup to restore from")
	databaseCmd.Flags().StringVarP(&dbNamespace, "namespace", "n", "", "Namespace the database lives in (required)")
	databaseCmd.Flags().StringVar(&dbSelector, "selector", "", "Label selector to narrow the restore (key=value[,key=value])")
	databaseCmd.Flags().StringVar(&dbRestoreName, "name", "", "Name for the Restore object (default: <backup>-restore-<timestamp>)")
}

func runDatabaseRestore(cmd *cobra.Command, args []string) error {
	src := resolveBackupName(args, dbFromBackup)
	if src == "" {
		return fmt.Errorf("a source backup is required (argument, --from-backup, or --backup)")
	}
	if dbNamespace == "" {
		return fmt.Errorf("--namespace is required to scope the database restore")
	}

	name := dbRestoreName
	if name == "" {
		name = defaultRestoreName(src)
	}

	fmt.Printf("🗄️  Database restore %q from backup %q (namespace %q)\n", name, src, dbNamespace)

	spec := map[string]interface{}{
		"backupName":         src,
		"includedNamespaces": []interface{}{dbNamespace},
		"restorePVs":         true,
	}
	if sel, err := parseSelector(dbSelector); err != nil {
		return err
	} else if len(sel) > 0 {
		spec["labelSelector"] = map[string]interface{}{"matchLabels": sel}
	}

	if dryRun {
		fmt.Println(helpers.CreateMuted(fmt.Sprintf("   DRY RUN - would restore namespace %q with selector %q", dbNamespace, dbSelector)))
		return nil
	}

	return createVeleroRestore(name, spec)
}
