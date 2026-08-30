package backup

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var (
	deleteCmd = &cobra.Command{
		Use:   "delete [backup-name]",
		Short: "Delete a Velero backup",
		Long: `Delete a Velero Backup (velero.io/v1) — removing it from the cluster and its
backing object storage. A single backup by name, or several by --pattern.

Examples:
  adhar backup delete my-backup
  adhar backup delete --pattern='adhar-backup-*' --force`,
		Args: cobra.MaximumNArgs(1),
		RunE: runDeleteBackup,
	}

	// Delete-specific flags
	forceDelete bool
	pattern     string
)

func init() {
	deleteCmd.Flags().BoolVarP(&forceDelete, "force", "f", false, "Force deletion without confirmation")
	deleteCmd.Flags().StringVarP(&pattern, "pattern", "p", "", "Delete backups whose name matches this glob pattern")
}

func runDeleteBackup(cmd *cobra.Command, args []string) error {
	if pattern != "" {
		return deleteBackupsByPattern(pattern)
	}
	if len(args) == 0 {
		return fmt.Errorf("a backup name is required (or use --pattern)")
	}
	return deleteSingleBackup(args[0])
}

func deleteSingleBackup(backupName string) error {
	dyn, err := getDynamicClient()
	if err != nil {
		return unreachable(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Confirm the backup exists so we can give a clear error before prompting.
	if _, err := dyn.Resource(backupGVR).Namespace(veleroNamespace).Get(ctx, backupName, metav1.GetOptions{}); err != nil {
		if crdMissing(err) {
			return fmt.Errorf("Velero Backup CRD not installed (velero not present in the cluster)")
		}
		return fmt.Errorf("backup not found: %s", backupName)
	}

	if !forceDelete && !confirm(fmt.Sprintf("🗑️  Are you sure you want to delete backup: %s? (y/N): ", backupName)) {
		fmt.Println("❌ Deletion cancelled")
		return nil
	}

	if err := dyn.Resource(backupGVR).Namespace(veleroNamespace).Delete(ctx, backupName, metav1.DeleteOptions{}); err != nil {
		return fmt.Errorf("failed to delete backup %q: %w", backupName, err)
	}

	fmt.Println(helpers.CreateSuccess(fmt.Sprintf("✅ Successfully deleted backup: %s", backupName)))
	return nil
}

func deleteBackupsByPattern(pat string) error {
	dyn, err := getDynamicClient()
	if err != nil {
		return unreachable(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	list, err := dyn.Resource(backupGVR).Namespace(veleroNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		if crdMissing(err) {
			return fmt.Errorf("Velero Backup CRD not installed (velero not present in the cluster)")
		}
		return fmt.Errorf("failed to list Velero backups: %w", err)
	}

	var matching []string
	for _, item := range list.Items {
		if ok, err := filepath.Match(pat, item.GetName()); err == nil && ok {
			matching = append(matching, item.GetName())
		}
	}

	if len(matching) == 0 {
		fmt.Printf("📭 No backups found matching pattern: %s\n", pat)
		return nil
	}

	fmt.Printf("🔍 Found %d backups matching pattern '%s':\n", len(matching), pat)
	for _, name := range matching {
		fmt.Printf("  - %s\n", name)
	}

	if !forceDelete && !confirm(fmt.Sprintf("\n🗑️  Are you sure you want to delete these %d backups? (y/N): ", len(matching))) {
		fmt.Println("❌ Deletion cancelled")
		return nil
	}

	deleted := 0
	for _, name := range matching {
		if err := dyn.Resource(backupGVR).Namespace(veleroNamespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
			fmt.Printf("⚠️  Failed to delete %s: %v\n", name, err)
			continue
		}
		deleted++
	}

	fmt.Println(helpers.CreateSuccess(fmt.Sprintf("✅ Successfully deleted %d backups", deleted)))
	return nil
}

// confirm prompts the user and reports whether they answered yes.
func confirm(prompt string) bool {
	fmt.Print(prompt)
	var response string
	fmt.Scanln(&response)
	return response == "y" || response == "Y"
}
