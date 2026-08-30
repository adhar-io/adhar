package db

import (
	"context"
	"fmt"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var restoreCmd = &cobra.Command{
	Use:   "restore",
	Short: "Restore database",
	Long: `Restore a managed PostgreSQL database from a CloudNativePG backup.

This provisions a NEW CloudNativePG Cluster that bootstraps by recovering from an
existing Backup (postgresql.cnpg.io/v1). The source database's data is restored
into the new cluster; the original is left untouched. Use --target to name the
restored cluster (defaults to <name>-restored).

Examples:
  adhar db restore --name=myapp --backup=myapp-20260101-020000
  adhar db restore --name=myapp --backup=myapp-20260101-020000 --target=myapp-dr --size=2Gi`,
	RunE: runRestore,
}

var (
	restoreBackup string
	restoreTarget string
	restoreSize   string
	restoreImage  string
)

func init() {
	restoreCmd.Flags().StringVarP(&restoreBackup, "backup", "b", "", "Name of the CNPG Backup object to recover from")
	restoreCmd.Flags().StringVar(&restoreTarget, "target", "", "Name for the restored cluster (default <name>-restored)")
	restoreCmd.Flags().StringVar(&restoreSize, "size", "1Gi", "Storage size for the restored cluster")
	restoreCmd.Flags().StringVar(&restoreImage, "image", "", "Override the PostgreSQL image (defaults to CNPG's default)")
}

func runRestore(cmd *cobra.Command, args []string) error {
	if dbName == "" {
		return fmt.Errorf("--name is required for database restore")
	}
	if restoreBackup == "" {
		return fmt.Errorf("--backup is required (the CNPG Backup object name to recover from)")
	}

	target := restoreTarget
	if target == "" {
		target = dbName + "-restored"
	}

	ns := dbNamespace()
	logger.Info(fmt.Sprintf("🔄 Restoring database %s into new cluster %s from backup: %s", dbName, target, restoreBackup))

	client, err := getDynamicClient()
	if err != nil {
		return err
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	// Validate the source Backup exists before creating the recovery cluster.
	if _, err := client.Resource(cnpgBackupGVR).Namespace(ns).Get(ctx, restoreBackup, metav1.GetOptions{}); err != nil {
		if k8serrors.IsNotFound(err) {
			return fmt.Errorf("backup %q not found in namespace %q", restoreBackup, ns)
		}
		return fmt.Errorf("look up backup: %w", err)
	}

	spec := map[string]interface{}{
		"instances": int64(1),
		"storage": map[string]interface{}{
			"size": restoreSize,
		},
		"bootstrap": map[string]interface{}{
			"recovery": map[string]interface{}{
				"backup": map[string]interface{}{
					"name": restoreBackup,
				},
			},
		},
	}
	if restoreImage != "" {
		spec["imageName"] = restoreImage
	}

	cluster := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "postgresql.cnpg.io/v1",
		"kind":       "Cluster",
		"metadata": map[string]interface{}{
			"name":      target,
			"namespace": ns,
			"annotations": map[string]interface{}{
				"argocd.argoproj.io/sync-options": "ServerSideApply=true",
			},
			"labels": map[string]interface{}{
				"adhar.io/managed-by":             "adhar-cli",
				"platform.adhar.io/restored-from": dbName,
			},
		},
		"spec": spec,
	}}

	if _, err := client.Resource(cnpgClusterGVR).Namespace(ns).Create(ctx, cluster, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create recovery cluster: %w", err)
	}

	fmt.Println(helpers.CreateSuccess(fmt.Sprintf("Recovery cluster %s created in namespace %s (restoring from %s)", target, ns, restoreBackup)))
	fmt.Println(helpers.CreateMuted(fmt.Sprintf("   Watch it: kubectl -n %s get cluster %s -w", ns, target)))
	fmt.Println(helpers.CreateMuted(fmt.Sprintf("   Connection secret when ready: %s-app", target)))
	return nil
}
