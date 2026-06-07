package health

import (
	"fmt"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
)

var historyCmd = &cobra.Command{
	Use:   "history",
	Short: "View health history and trends",
	Long: `View historical health data and trends for the platform.
	
Examples:
  adhar health history
  adhar health history --days=7
  adhar health history --component=argocd`,
	RunE: runHistory,
}

var (
	historyDays      int
	historyComponent string
)

func init() {
	historyCmd.Flags().IntVarP(&historyDays, "days", "d", 30, "Number of days to show")
	historyCmd.Flags().StringVarP(&historyComponent, "component", "c", "", "Show history for specific component")
}

func runHistory(cmd *cobra.Command, args []string) error {
	logger.Info("📈 Viewing health history...")

	// Adhar does not persist historical health snapshots locally; trend data is
	// surfaced by the observability stack (kube-prometheus / Grafana) deployed
	// by the platform. Point the user there and show the current snapshot so the
	// command is still useful rather than a no-op.
	fmt.Println(helpers.CreateMuted(
		"Historical health trends are not stored by the CLI. " +
			"View time-series metrics in Grafana (kube-prometheus) at " +
			"https://adhar.localtest.me:8443/grafana."))
	fmt.Printf("\n%s\n", helpers.TitleStyle.Render("📸 Current Snapshot"))

	_, err := runHealthSweep(historyComponent, parseTimeout(timeout))
	return err
}
