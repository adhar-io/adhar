package backup

import (
	"context"
	"fmt"
	"time"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var (
	createCmd = &cobra.Command{
		Use:   "create [backup-name]",
		Short: "Create a new platform backup",
		Long: `Create an on-demand Velero Backup (velero.io/v1 Backup) of the platform.

The backup is a first-class Velero object, the same backend used by
'adhar backup list/status/verify/schedule' and 'adhar restore'. Scope the
backup with --include-namespaces / --exclude-namespaces and set a retention
window with --ttl.

Examples:
  adhar backup create                                   # back up everything
  adhar backup create my-backup                         # named backup
  adhar backup create --include-namespaces=team-a,team-b
  adhar backup create prod-nightly --ttl=168h`,
		Args: cobra.MaximumNArgs(1),
		RunE: runCreateBackup,
	}

	// Create-specific flags
	backupType        string
	backupName        string
	description       string
	includeNamespaces []string
	excludeNamespaces []string
	backupTTL         time.Duration
	storageLocation   string
)

func init() {
	createCmd.Flags().StringVarP(&backupType, "type", "t", "full", "Backup type: full or selective")
	createCmd.Flags().StringVarP(&description, "description", "", "", "Backup description (stored as an annotation)")
	createCmd.Flags().StringSliceVarP(&includeNamespaces, "include-namespaces", "", nil, "Namespaces to include (default: all)")
	createCmd.Flags().StringSliceVarP(&excludeNamespaces, "exclude-namespaces", "x", nil, "Namespaces to exclude")
	createCmd.Flags().DurationVarP(&backupTTL, "ttl", "", 720*time.Hour, "Retention period before the backup is garbage-collected")
	createCmd.Flags().StringVarP(&storageLocation, "storage-location", "", "", "Velero BackupStorageLocation to use (default: Velero's default)")
}

func runCreateBackup(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		backupName = args[0]
	} else {
		backupName = fmt.Sprintf("adhar-backup-%s", time.Now().Format("2006-01-02-150405"))
	}

	fmt.Printf("🔒 Creating Velero backup: %s\n", backupName)
	if len(includeNamespaces) > 0 {
		fmt.Printf("📦 Include namespaces: %v\n", includeNamespaces)
	} else {
		fmt.Println("📦 Include namespaces: all")
	}
	if len(excludeNamespaces) > 0 {
		fmt.Printf("🚫 Exclude namespaces: %v\n", excludeNamespaces)
	}
	fmt.Printf("⏳ TTL: %s\n", backupTTL)

	dyn, err := getDynamicClient()
	if err != nil {
		return unreachable(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	spec := map[string]interface{}{
		"ttl": fmt.Sprintf("%dh0m0s", int(backupTTL.Hours())),
	}
	if len(includeNamespaces) > 0 {
		spec["includedNamespaces"] = toIfaceSlice(includeNamespaces)
	}
	if len(excludeNamespaces) > 0 {
		spec["excludedNamespaces"] = toIfaceSlice(excludeNamespaces)
	}
	if storageLocation != "" {
		spec["storageLocation"] = storageLocation
	}

	annotations := map[string]interface{}{}
	if description != "" {
		annotations["adhar.io/description"] = description
	}
	if backupType != "" {
		annotations["adhar.io/backup-type"] = backupType
	}

	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "velero.io/v1",
		"kind":       "Backup",
		"metadata": map[string]interface{}{
			"name":        backupName,
			"namespace":   veleroNamespace,
			"labels":      map[string]interface{}{"adhar.io/managed-by": "adhar-cli"},
			"annotations": annotations,
		},
		"spec": spec,
	}}

	if _, err := dyn.Resource(backupGVR).Namespace(veleroNamespace).Create(ctx, obj, metav1.CreateOptions{}); err != nil {
		if crdMissing(err) {
			return fmt.Errorf("Velero Backup CRD not installed (velero not present in the cluster)")
		}
		return fmt.Errorf("failed to create backup %q: %w", backupName, err)
	}

	fmt.Println(helpers.CreateSuccess(fmt.Sprintf("✅ Backup %q created", backupName)))
	fmt.Println(helpers.CreateMuted("   Track progress with: adhar backup status " + backupName))
	return nil
}

// toIfaceSlice converts a []string to []interface{} for unstructured specs.
func toIfaceSlice(in []string) []interface{} {
	out := make([]interface{}, 0, len(in))
	for _, s := range in {
		out = append(out, s)
	}
	return out
}
