package env

import (
	"context"
	"fmt"
	"time"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var restoreCmd = &cobra.Command{
	Use:   "restore [environment-name] [backup-name]",
	Short: "Restore environment from backup",
	Long: `Restore an environment's namespace from a Velero backup by creating a Velero
Restore (velero.io/v1) that targets the given backup, scoped to the environment
namespace.

Examples:
  adhar env restore dev dev-20260101-020000
  adhar env restore prod prod-20260101-020000 --velero-namespace=velero`,
	Args: cobra.ExactArgs(2),
	RunE: runRestore,
}

var restoreVeleroNS string

func init() {
	restoreCmd.Flags().StringVar(&restoreVeleroNS, "velero-namespace", "velero", "Namespace where Velero runs")
}

func runRestore(cmd *cobra.Command, args []string) error {
	envName := args[0]
	backupName := args[1]
	restoreName := fmt.Sprintf("%s-restore-%s", envName, time.Now().UTC().Format("20060102-150405"))
	logger.Info(fmt.Sprintf("🔄 Restoring environment %s from backup %s", envName, backupName))

	dyn, err := getDynamicClient()
	if err != nil {
		return unreachable(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Validate the backup exists before creating the restore.
	if _, err := dyn.Resource(veleroBackupGVR).Namespace(restoreVeleroNS).Get(ctx, backupName, metav1.GetOptions{}); err != nil {
		if crdMissing(err) {
			return fmt.Errorf("Velero is not installed (velero.io/v1 CRDs missing); enable the velero package")
		}
		if k8serrors.IsNotFound(err) {
			return fmt.Errorf("backup %q not found in namespace %q", backupName, restoreVeleroNS)
		}
		return fmt.Errorf("look up backup: %w", err)
	}

	restore := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "velero.io/v1",
		"kind":       "Restore",
		"metadata": map[string]interface{}{
			"name":      restoreName,
			"namespace": restoreVeleroNS,
			"labels": map[string]interface{}{
				"adhar.io/managed-by":  "adhar-cli",
				"adhar.io/environment": envName,
			},
		},
		"spec": map[string]interface{}{
			"backupName":         backupName,
			"includedNamespaces": []interface{}{envName},
		},
	}}

	if _, err := dyn.Resource(veleroRestoreGVR).Namespace(restoreVeleroNS).Create(ctx, restore, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create restore: %w", err)
	}

	fmt.Println(helpers.CreateSuccess(fmt.Sprintf("✅ Restore %s created for environment %q (from %s)", restoreName, envName, backupName)))
	fmt.Println(helpers.CreateMuted(fmt.Sprintf("   Track it: kubectl -n %s get restore %s -o wide", restoreVeleroNS, restoreName)))
	return nil
}
