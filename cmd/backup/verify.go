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

var verifyCmd = &cobra.Command{
	Use:   "verify [backup-name]",
	Short: "Verify a Velero backup's status",
	Long: `Verify a Velero Backup by inspecting its phase and error/warning counts.
A non-Completed phase results in a non-zero exit so this is CI-friendly.

Examples:
  adhar backup verify my-backup`,
	Args: cobra.ExactArgs(1),
	RunE: runVerifyBackup,
}

func runVerifyBackup(cmd *cobra.Command, args []string) error {
	name := args[0]
	fmt.Printf("🔍 Verifying backup: %s\n", name)

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

	phase := nestedString(obj.Object, "status", "phase")
	errCount := countNested(obj.Object, "status", "errors")
	warnCount := countNested(obj.Object, "status", "warnings")
	started := nestedString(obj.Object, "status", "startTimestamp")
	completed := nestedString(obj.Object, "status", "completionTimestamp")

	var b strings.Builder
	b.WriteString(fmt.Sprintf("📦 Name:      %s\n", name))
	b.WriteString(fmt.Sprintf("📊 Phase:     %s\n", phaseIcon(phase)))
	b.WriteString(fmt.Sprintf("⚠️  Warnings:  %d\n", warnCount))
	b.WriteString(fmt.Sprintf("❌ Errors:    %d\n", errCount))
	if started != "" {
		b.WriteString(fmt.Sprintf("🕐 Started:   %s\n", started))
	}
	if completed != "" {
		b.WriteString(fmt.Sprintf("🏁 Completed: %s", completed))
	}
	fmt.Println(helpers.BorderStyle.Width(70).Render(b.String()))

	if phase != "Completed" {
		return fmt.Errorf("backup %q is not Completed (phase: %s)", name, phase)
	}
	fmt.Println(helpers.CreateSuccess("✅ Backup verified: phase Completed"))
	return nil
}
