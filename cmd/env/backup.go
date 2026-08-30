package env

import (
	"context"
	"fmt"
	"time"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var backupCmd = &cobra.Command{
	Use:   "backup [environment-name]",
	Short: "Backup environment",
	Long: `Back up an environment's namespace by creating a Velero Backup (velero.io/v1)
scoped to the environment namespace. Velero snapshots the namespace's resources
(and volumes, if a volume snapshotter is configured).

Examples:
  adhar env backup dev
  adhar env backup prod --ttl=336h0m0s
  adhar env backup dev --velero-namespace=velero`,
	Args: cobra.ExactArgs(1),
	RunE: runBackup,
}

var (
	backupTTL      string
	backupVeleroNS string
	backupStorage  string
)

func init() {
	backupCmd.Flags().StringVar(&backupTTL, "ttl", "720h0m0s", "Backup retention (Velero TTL)")
	backupCmd.Flags().StringVar(&backupVeleroNS, "velero-namespace", "velero", "Namespace where Velero runs")
	backupCmd.Flags().StringVar(&backupStorage, "storage-location", "default", "Velero backup storage location")
}

func runBackup(cmd *cobra.Command, args []string) error {
	envName := args[0]
	backupName := fmt.Sprintf("%s-%s", envName, time.Now().UTC().Format("20060102-150405"))
	logger.Info(fmt.Sprintf("💾 Creating Velero backup %s for environment: %s", backupName, envName))

	dyn, err := getDynamicClient()
	if err != nil {
		return unreachable(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	backup := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "velero.io/v1",
		"kind":       "Backup",
		"metadata": map[string]interface{}{
			"name":      backupName,
			"namespace": backupVeleroNS,
			"labels": map[string]interface{}{
				"adhar.io/managed-by":  "adhar-cli",
				"adhar.io/environment": envName,
			},
		},
		"spec": map[string]interface{}{
			"includedNamespaces": []interface{}{envName},
			"ttl":                backupTTL,
			"storageLocation":    backupStorage,
		},
	}}

	if _, err := dyn.Resource(veleroBackupGVR).Namespace(backupVeleroNS).Create(ctx, backup, metav1.CreateOptions{}); err != nil {
		if crdMissing(err) {
			return fmt.Errorf("Velero is not installed (velero.io/v1 Backup CRD missing); enable the velero package")
		}
		return fmt.Errorf("create backup: %w", err)
	}

	fmt.Println(helpers.CreateSuccess(fmt.Sprintf("✅ Backup %s created for environment %q", backupName, envName)))
	fmt.Println(helpers.CreateMuted(fmt.Sprintf("   Track it: kubectl -n %s get backup %s -o wide", backupVeleroNS, backupName)))
	fmt.Println(helpers.CreateMuted(fmt.Sprintf("   Restore:  adhar env restore %s %s", envName, backupName)))
	return nil
}
