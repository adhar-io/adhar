package health

import (
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
)

var checkCmd = &cobra.Command{
	Use:   "check",
	Short: "Run specific health checks",
	Long: `Run specific health checks on the platform.
	
Examples:
  adhar health check --component=argocd
  adhar health check --namespace=default
  adhar health check --all`,
	RunE: runCheck,
}

var (
	checkAll bool
)

func init() {
	checkCmd.Flags().BoolVar(&checkAll, "all", false, "Run all health checks")
}

func runCheck(cmd *cobra.Command, args []string) error {
	logger.Info("🔍 Running health checks...")

	if checkAll || component == "" {
		return runAllHealthChecks()
	}

	_, err := runHealthSweep(component, parseTimeout(timeout))
	return err
}

func runAllHealthChecks() error {
	logger.Info("🔍 Running all health checks...")

	_, err := runHealthSweep("", parseTimeout(timeout))
	return err
}
