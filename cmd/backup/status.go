package backup

import (
	"context"
	"fmt"
	"strings"
	"time"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var statusCmd = &cobra.Command{
	Use:   "status [backup-name]",
	Short: "Show Velero backup status",
	Long: `Show Velero backup status. With no argument, prints a phase summary across
all backups; with a backup name, prints that backup's detailed status.

Examples:
  adhar backup status               # Summary of all backups by phase
  adhar backup status my-backup     # Detailed status for one backup`,
	Args: cobra.MaximumNArgs(1),
	RunE: runBackupStatus,
}

func init() {
	BackupCmd.AddCommand(statusCmd)
}

func runBackupStatus(cmd *cobra.Command, args []string) error {
	if len(args) == 1 {
		// Reuse verify's detailed read without the non-zero exit semantics.
		return showSingleBackupStatus(args[0])
	}

	fmt.Println(helpers.TitleStyle.Render("📊 Velero Backup Status Summary"))

	rows, err := fetchBackups(context.Background())
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Println(helpers.CreateMuted("   No backups found"))
		return nil
	}

	counts := map[string]int{}
	for _, r := range rows {
		phase := r.rawPhase
		if phase == "" {
			phase = "Unknown"
		}
		counts[phase]++
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("Total backups: %d\n", len(rows)))
	for _, phase := range []string{"Completed", "InProgress", "New", "PartiallyFailed", "Failed", "FailedValidation", "Unknown"} {
		if c, ok := counts[phase]; ok {
			b.WriteString(fmt.Sprintf("  %-18s %d\n", phase+":", c))
			delete(counts, phase)
		}
	}
	for phase, c := range counts {
		b.WriteString(fmt.Sprintf("  %-18s %d\n", phase+":", c))
	}
	fmt.Println(helpers.BorderStyle.Width(60).Render(strings.TrimRight(b.String(), "\n")))
	return nil
}

func showSingleBackupStatus(name string) error {
	dyn, err := getDynamicClient()
	if err != nil {
		return unreachable(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	obj, err := dyn.Resource(backupGVR).Namespace(veleroNamespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if crdMissing(err) {
			return fmt.Errorf("Velero Backup CRD not installed (velero not present in the cluster)")
		}
		return fmt.Errorf("failed to get backup %q: %w", name, err)
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("📦 Name:      %s\n", name))
	b.WriteString(fmt.Sprintf("📊 Phase:     %s\n", phaseIcon(nestedString(obj.Object, "status", "phase"))))
	b.WriteString(fmt.Sprintf("⚠️  Warnings:  %d\n", countNested(obj.Object, "status", "warnings")))
	b.WriteString(fmt.Sprintf("❌ Errors:    %d\n", countNested(obj.Object, "status", "errors")))
	b.WriteString(fmt.Sprintf("🕐 Created:   %s\n", backupAge(obj.GetCreationTimestamp().Time)))
	if exp := nestedString(obj.Object, "status", "expiration"); exp != "" {
		b.WriteString(fmt.Sprintf("⏳ Expires:   %s", exp))
	}
	fmt.Println(helpers.BorderStyle.Width(70).Render(strings.TrimRight(b.String(), "\n")))
	return nil
}
