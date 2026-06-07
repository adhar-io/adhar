package backup

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var (
	listCmd = &cobra.Command{
		Use:   "list",
		Short: "List existing backups",
		Long: `List Velero Backups (velero.io/v1) in the cluster, with phase and timing.

Examples:
  adhar backup list                 # All Velero backups
  adhar backup list --limit=10      # Show the 10 most recent`,
		RunE: runListBackups,
	}

	// List-specific flags
	showDetails bool
	sortBy      string
	limit       int
)

func init() {
	listCmd.Flags().BoolVarP(&showDetails, "detailed", "", false, "Show detailed backup information")
	listCmd.Flags().StringVarP(&sortBy, "sort", "s", "date", "Sort by: date or name")
	listCmd.Flags().IntVarP(&limit, "limit", "l", 0, "Limit number of backups to show")
}

type backupRow struct {
	Name       string
	Phase      string
	Created    time.Time
	Expires    string
	Errors     string
	Warnings   string
	StorageLoc string
	rawPhase   string
}

func runListBackups(cmd *cobra.Command, args []string) error {
	fmt.Println(helpers.TitleStyle.Render("📋 Velero Backups"))

	rows, err := fetchBackups(context.Background())
	if err != nil {
		return err
	}

	switch sortBy {
	case "name":
		sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	default:
		sort.Slice(rows, func(i, j int) bool { return rows[i].Created.After(rows[j].Created) })
	}
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}

	if len(rows) == 0 {
		fmt.Println(helpers.CreateMuted("   No backups found"))
		return nil
	}

	var b strings.Builder
	if showDetails {
		b.WriteString(fmt.Sprintf("%-34s %-18s %-8s %-6s %-6s %-12s\n", "NAME", "PHASE", "AGE", "ERR", "WARN", "STORAGE"))
		b.WriteString(strings.Repeat("─", 90) + "\n")
		for _, r := range rows {
			b.WriteString(fmt.Sprintf("%-34s %-18s %-8s %-6s %-6s %-12s\n",
				truncate(r.Name, 32), phaseIcon(r.rawPhase), backupAge(r.Created), r.Errors, r.Warnings, truncate(r.StorageLoc, 10)))
		}
	} else {
		b.WriteString(fmt.Sprintf("%-40s %-18s %-8s\n", "NAME", "PHASE", "AGE"))
		b.WriteString(strings.Repeat("─", 70) + "\n")
		for _, r := range rows {
			b.WriteString(fmt.Sprintf("%-40s %-18s %-8s\n",
				truncate(r.Name, 38), phaseIcon(r.rawPhase), backupAge(r.Created)))
		}
	}
	fmt.Print(b.String())
	return nil
}

// fetchBackups lists Velero Backup CRs and maps them to display rows. A missing
// CRD returns a clear, non-panicking error.
func fetchBackups(ctx context.Context) ([]backupRow, error) {
	dyn, err := getDynamicClient()
	if err != nil {
		return nil, unreachable(err)
	}

	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	list, err := dyn.Resource(backupGVR).Namespace(veleroNamespace).List(cctx, metav1.ListOptions{})
	if err != nil {
		if crdMissing(err) {
			return nil, fmt.Errorf("Velero Backup CRD not installed (velero not present in the cluster)")
		}
		return nil, fmt.Errorf("failed to list Velero backups: %w", err)
	}

	rows := make([]backupRow, 0, len(list.Items))
	for _, item := range list.Items {
		phase := nestedString(item.Object, "status", "phase")
		rows = append(rows, backupRow{
			Name:       item.GetName(),
			Phase:      phaseIcon(phase),
			rawPhase:   phase,
			Created:    item.GetCreationTimestamp().Time,
			Expires:    nestedString(item.Object, "status", "expiration"),
			Errors:     fmt.Sprintf("%d", countNested(item.Object, "status", "errors")),
			Warnings:   fmt.Sprintf("%d", countNested(item.Object, "status", "warnings")),
			StorageLoc: nestedString(item.Object, "spec", "storageLocation"),
		})
	}
	return rows, nil
}

func countNested(obj map[string]interface{}, fields ...string) int64 {
	v, found, _ := unstructuredNestedInt(obj, fields...)
	if !found {
		return 0
	}
	return v
}
