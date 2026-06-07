package restore

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// listRestoreCmd lists Velero Restore objects.
var listRestoreCmd = &cobra.Command{
	Use:   "list",
	Short: "List Velero restores",
	Long: `List Velero Restore objects (velero.io/v1) and their phase.

Examples:
  adhar restore list`,
	RunE: runListRestores,
}

// createRestoreCmd creates a Velero Restore from a backup.
var (
	createRestoreCmd = &cobra.Command{
		Use:   "create [restore-name]",
		Short: "Create a Velero restore from a backup",
		Long: `Create a Velero Restore from an existing backup. The source backup is given
via --from-backup (or the global --backup flag).

Examples:
  adhar restore create --from-backup=my-backup
  adhar restore create my-restore --from-backup=my-backup`,
		Args: cobra.MaximumNArgs(1),
		RunE: runCreateRestore,
	}

	fromBackup string
)

// statusRestoreCmd shows restore status.
var statusRestoreCmd = &cobra.Command{
	Use:   "status [restore-name]",
	Short: "Show Velero restore status",
	Long: `Show Velero restore status. With no argument, prints a phase summary; with a
name, prints detailed status for that restore.

Examples:
  adhar restore status
  adhar restore status my-restore`,
	Args: cobra.MaximumNArgs(1),
	RunE: runRestoreStatus,
}

func init() {
	createRestoreCmd.Flags().StringVar(&fromBackup, "from-backup", "", "Name of the Velero backup to restore from")

	RestoreCmd.AddCommand(listRestoreCmd)
	RestoreCmd.AddCommand(createRestoreCmd)
	RestoreCmd.AddCommand(statusRestoreCmd)
}

type restoreRow struct {
	Name     string
	Backup   string
	Phase    string
	rawPhase string
	Created  time.Time
	Errors   int64
	Warnings int64
}

func fetchRestores(ctx context.Context) ([]restoreRow, error) {
	dyn, err := getDynamicClient()
	if err != nil {
		return nil, unreachable(err)
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	list, err := dyn.Resource(restoreGVR).Namespace(veleroNamespace).List(cctx, metav1.ListOptions{})
	if err != nil {
		if crdMissing(err) {
			return nil, fmt.Errorf("Velero Restore CRD not installed (velero not present in the cluster)")
		}
		return nil, fmt.Errorf("failed to list Velero restores: %w", err)
	}

	rows := make([]restoreRow, 0, len(list.Items))
	for _, item := range list.Items {
		phase := nestedString(item.Object, "status", "phase")
		rows = append(rows, restoreRow{
			Name:     item.GetName(),
			Backup:   nestedString(item.Object, "spec", "backupName"),
			Phase:    phaseIcon(phase),
			rawPhase: phase,
			Created:  item.GetCreationTimestamp().Time,
			Errors:   countNested(item.Object, "status", "errors"),
			Warnings: countNested(item.Object, "status", "warnings"),
		})
	}
	return rows, nil
}

func runListRestores(cmd *cobra.Command, args []string) error {
	fmt.Println(helpers.TitleStyle.Render("📋 Velero Restores"))

	rows, err := fetchRestores(context.Background())
	if err != nil {
		return err
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Created.After(rows[j].Created) })

	if len(rows) == 0 {
		fmt.Println(helpers.CreateMuted("   No restores found"))
		return nil
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("%-32s %-28s %-16s %-8s\n", "NAME", "BACKUP", "PHASE", "AGE"))
	b.WriteString(strings.Repeat("─", 88) + "\n")
	for _, r := range rows {
		b.WriteString(fmt.Sprintf("%-32s %-28s %-16s %-8s\n",
			truncate(r.Name, 30), truncate(r.Backup, 26), phaseIcon(r.rawPhase), restoreAge(r.Created)))
	}
	fmt.Print(b.String())
	return nil
}

func runCreateRestore(cmd *cobra.Command, args []string) error {
	src := fromBackup
	if src == "" {
		src = backupPath // fall back to global --backup flag
	}
	if src == "" {
		return fmt.Errorf("a source backup is required: use --from-backup=<backup-name>")
	}

	name := ""
	if len(args) > 0 {
		name = args[0]
	}
	if name == "" {
		name = fmt.Sprintf("%s-restore-%s", src, time.Now().Format("20060102-150405"))
	}

	fmt.Printf("🔄 Creating Velero restore %q from backup %q\n", name, src)

	if dryRun {
		fmt.Println(helpers.CreateMuted("   DRY RUN - no Restore object created"))
		return nil
	}

	dyn, err := getDynamicClient()
	if err != nil {
		return unreachable(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "velero.io/v1",
		"kind":       "Restore",
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": veleroNamespace,
			"labels":    map[string]interface{}{"adhar.io/managed-by": "adhar-cli"},
		},
		"spec": map[string]interface{}{
			"backupName": src,
		},
	}}

	if _, err := dyn.Resource(restoreGVR).Namespace(veleroNamespace).Create(ctx, obj, metav1.CreateOptions{}); err != nil {
		if crdMissing(err) {
			return fmt.Errorf("Velero Restore CRD not installed (velero not present in the cluster)")
		}
		return fmt.Errorf("failed to create restore %q: %w", name, err)
	}
	fmt.Println(helpers.CreateSuccess(fmt.Sprintf("✅ Restore %q created from backup %q", name, src)))
	fmt.Println(helpers.CreateMuted("   Track progress with: adhar restore status " + name))
	return nil
}

func runRestoreStatus(cmd *cobra.Command, args []string) error {
	if len(args) == 1 {
		return showSingleRestoreStatus(args[0])
	}

	fmt.Println(helpers.TitleStyle.Render("📊 Velero Restore Status Summary"))
	rows, err := fetchRestores(context.Background())
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Println(helpers.CreateMuted("   No restores found"))
		return nil
	}

	counts := map[string]int{}
	for _, r := range rows {
		p := r.rawPhase
		if p == "" {
			p = "Unknown"
		}
		counts[p]++
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Total restores: %d\n", len(rows)))
	for phase, c := range counts {
		b.WriteString(fmt.Sprintf("  %-18s %d\n", phase+":", c))
	}
	fmt.Println(helpers.BorderStyle.Width(60).Render(strings.TrimRight(b.String(), "\n")))
	return nil
}

func showSingleRestoreStatus(name string) error {
	dyn, err := getDynamicClient()
	if err != nil {
		return unreachable(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	obj, err := dyn.Resource(restoreGVR).Namespace(veleroNamespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if crdMissing(err) {
			return fmt.Errorf("Velero Restore CRD not installed (velero not present in the cluster)")
		}
		return fmt.Errorf("failed to get restore %q: %w", name, err)
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("🔄 Name:      %s\n", name))
	b.WriteString(fmt.Sprintf("📦 Backup:    %s\n", nestedString(obj.Object, "spec", "backupName")))
	b.WriteString(fmt.Sprintf("📊 Phase:     %s\n", phaseIcon(nestedString(obj.Object, "status", "phase"))))
	b.WriteString(fmt.Sprintf("⚠️  Warnings:  %d\n", countNested(obj.Object, "status", "warnings")))
	b.WriteString(fmt.Sprintf("❌ Errors:    %d\n", countNested(obj.Object, "status", "errors")))
	b.WriteString(fmt.Sprintf("🕐 Created:   %s", restoreAge(obj.GetCreationTimestamp().Time)))
	fmt.Println(helpers.BorderStyle.Width(70).Render(b.String()))
	return nil
}
