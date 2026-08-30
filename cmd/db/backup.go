package db

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

var backupCmd = &cobra.Command{
	Use:   "backup",
	Short: "Backup database",
	Long: `Create an on-demand backup of a managed PostgreSQL database.

This creates a CloudNativePG "Backup" (postgresql.cnpg.io/v1) that targets the
Cluster the CompositeDatabase composed. The backup runs through the same
control-plane-managed resource the Console/GitOps would create — CNPG performs
the backup according to the target Cluster's configured backup method.

Examples:
  adhar db backup --name=myapp
  adhar db backup --name=myapp --method=volumeSnapshot
  adhar db backup --name=myapp --namespace=team-a`,
	RunE: runBackup,
}

var backupMethod string

func init() {
	backupCmd.Flags().StringVar(&backupMethod, "method", "barmanObjectStore",
		"CNPG backup method (barmanObjectStore, volumeSnapshot, plugin)")
}

func runBackup(cmd *cobra.Command, args []string) error {
	if dbName == "" {
		return fmt.Errorf("--name is required for database backup")
	}

	ns := dbNamespace()
	backupName := fmt.Sprintf("%s-%s", dbName, time.Now().UTC().Format("20060102-150405"))
	logger.Info(fmt.Sprintf("💾 Creating backup %s for database: %s (method: %s)", backupName, dbName, backupMethod))

	client, err := getDynamicClient()
	if err != nil {
		return err
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	// Verify the composed CNPG Cluster exists so we fail early with a clear
	// message rather than creating a dangling Backup.
	if _, err := client.Resource(cnpgClusterGVR).Namespace(ns).Get(ctx, dbName, metav1.GetOptions{}); err != nil {
		if k8serrors.IsNotFound(err) {
			return fmt.Errorf("no PostgreSQL cluster %q found in namespace %q (is this a CNPG-backed database?)", dbName, ns)
		}
		return fmt.Errorf("look up database cluster: %w", err)
	}

	backup := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "postgresql.cnpg.io/v1",
		"kind":       "Backup",
		"metadata": map[string]interface{}{
			"name":      backupName,
			"namespace": ns,
			"labels": map[string]interface{}{
				"adhar.io/managed-by":        "adhar-cli",
				"platform.adhar.io/database": dbName,
			},
		},
		"spec": map[string]interface{}{
			"method": backupMethod,
			"cluster": map[string]interface{}{
				"name": dbName,
			},
		},
	}}

	if _, err := client.Resource(cnpgBackupGVR).Namespace(ns).Create(ctx, backup, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create backup: %w", err)
	}

	fmt.Println(helpers.CreateSuccess(fmt.Sprintf("Backup %s requested for database %s (namespace %s)", backupName, dbName, ns)))
	fmt.Println(helpers.CreateMuted(fmt.Sprintf("   Track it: kubectl -n %s get backup %s -o wide", ns, backupName)))
	return nil
}
