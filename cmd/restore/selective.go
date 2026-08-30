package restore

import (
	"fmt"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
)

var (
	selectiveCmd = &cobra.Command{
		Use:   "selective [backup-name]",
		Short: "Selective component restoration",
		Long: `Restore specific components from a Velero backup by namespace and/or label
selector. Creates a velero.io/v1 Restore narrowed to the requested namespaces,
resources, and labels.

Examples:
  adhar restore selective my-backup --include-namespaces=team-a,team-b
  adhar restore selective --from-backup=nightly --selector=app.kubernetes.io/part-of=argocd
  adhar restore selective my-backup --include-resources=deployments,services`,
		Args: cobra.MaximumNArgs(1),
		RunE: runSelectiveRestore,
	}

	// Selective restore specific flags
	selFromBackup  string
	selNamespaces  []string
	selResources   []string
	selSelector    string
	selRestoreName string
)

func init() {
	selectiveCmd.Flags().StringVar(&selFromBackup, "from-backup", "", "Name of the Velero backup to restore from")
	selectiveCmd.Flags().StringSliceVar(&selNamespaces, "include-namespaces", nil, "Namespaces to restore")
	selectiveCmd.Flags().StringSliceVar(&selResources, "include-resources", nil, "Resource types to restore (e.g. deployments,services)")
	selectiveCmd.Flags().StringVar(&selSelector, "selector", "", "Label selector (key=value[,key=value])")
	selectiveCmd.Flags().StringVar(&selRestoreName, "name", "", "Name for the Restore object (default: <backup>-restore-<timestamp>)")
}

func runSelectiveRestore(cmd *cobra.Command, args []string) error {
	src := resolveBackupName(args, selFromBackup)
	if src == "" {
		return fmt.Errorf("a source backup is required (argument, --from-backup, or --backup)")
	}
	if len(selNamespaces) == 0 && len(selResources) == 0 && selSelector == "" {
		return fmt.Errorf("selective restore needs at least one of --include-namespaces, --include-resources, or --selector")
	}

	name := selRestoreName
	if name == "" {
		name = defaultRestoreName(src)
	}

	fmt.Printf("🔄 Selective restore %q from backup %q\n", name, src)

	spec := map[string]interface{}{
		"backupName": src,
		"restorePVs": true,
	}
	if len(selNamespaces) > 0 {
		spec["includedNamespaces"] = toIfaceSlice(selNamespaces)
		fmt.Printf("📦 Namespaces: %v\n", selNamespaces)
	}
	if len(selResources) > 0 {
		spec["includedResources"] = toIfaceSlice(selResources)
		fmt.Printf("🔧 Resources: %v\n", selResources)
	}
	if sel, err := parseSelector(selSelector); err != nil {
		return err
	} else if len(sel) > 0 {
		spec["labelSelector"] = map[string]interface{}{"matchLabels": sel}
		fmt.Printf("🏷️  Selector: %s\n", selSelector)
	}

	if dryRun {
		fmt.Println(helpers.CreateMuted("   DRY RUN - no Restore object created"))
		return nil
	}

	return createVeleroRestore(name, spec)
}
