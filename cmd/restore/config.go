package restore

import (
	"context"
	"fmt"
	"strings"
	"time"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Show restore/backup storage configuration",
	Long: `Show the Velero backup/restore configuration by reading the
BackupStorageLocations (velero.io/v1) the cluster restores from. This is where
backups are read/written, so it tells you what a restore will pull from.

Examples:
  adhar restore config`,
	RunE: runConfigRestore,
}

func runConfigRestore(cmd *cobra.Command, args []string) error {
	fmt.Println(helpers.TitleStyle.Render("⚙️  Velero Backup Storage Locations"))

	dyn, err := getDynamicClient()
	if err != nil {
		return unreachable(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	list, err := dyn.Resource(backupStorageLocationGVR).Namespace(veleroNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		if crdMissing(err) {
			return fmt.Errorf("Velero BackupStorageLocation CRD not installed (velero not present in the cluster)")
		}
		return fmt.Errorf("failed to list backup storage locations: %w", err)
	}

	if len(list.Items) == 0 {
		fmt.Println(helpers.CreateMuted("   No backup storage locations configured"))
		return nil
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("%-24s %-10s %-12s %-28s %-10s\n", "NAME", "PROVIDER", "PHASE", "BUCKET", "DEFAULT"))
	b.WriteString(strings.Repeat("─", 88) + "\n")
	for _, item := range list.Items {
		def, _, _ := unstructuredBool(item.Object, "spec", "default")
		b.WriteString(fmt.Sprintf("%-24s %-10s %-12s %-28s %-10t\n",
			truncate(item.GetName(), 22),
			truncate(nestedString(item.Object, "spec", "provider"), 8),
			phaseIcon(nestedString(item.Object, "status", "phase")),
			truncate(nestedString(item.Object, "spec", "objectStorage", "bucket"), 26),
			def,
		))
	}
	fmt.Print(b.String())
	return nil
}
