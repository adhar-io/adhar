package health

import (
	"fmt"
	"os"
	"strings"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
)

var reportCmd = &cobra.Command{
	Use:   "report",
	Short: "Generate health reports",
	Long: `Generate comprehensive health reports for the platform.
	
Examples:
  adhar health report
  adhar health report --format=json
  adhar health report --output=health-report.html`,
	RunE: runReport,
}

var (
	reportFormat string
	reportOutput string
)

func init() {
	reportCmd.Flags().StringVarP(&reportFormat, "format", "f", "table", "Report format (table, json, yaml, html)")
	reportCmd.Flags().StringVarP(&reportOutput, "output", "o", "", "Output file path")
}

func runReport(cmd *cobra.Command, args []string) error {
	logger.Info("📊 Generating health report...")

	clientset, err := getClientset()
	if err != nil {
		fmt.Println(helpers.ErrorStyle.Render("❌ Could not connect to the cluster"))
		fmt.Println(helpers.CreateMuted("   " + err.Error()))
		return fmt.Errorf("failed to get Kubernetes client: %w", err)
	}

	components, err := resolveComponents(component)
	if err != nil {
		return err
	}

	h := collectPlatformHealth(clientset, components, parseTimeout(timeout))

	switch strings.ToLower(reportFormat) {
	case "json":
		if reportOutput != "" {
			return writeReportFile(reportOutput, mustJSON(h))
		}
		return helpers.PrintJSON(h)
	case "yaml":
		if reportOutput != "" {
			return writeReportFile(reportOutput, mustYAML(h))
		}
		return helpers.PrintYAML(h)
	default:
		// Table (default). When an output file is requested, write the plain
		// summary lines so the report is machine-friendly.
		if reportOutput != "" {
			return writeReportFile(reportOutput, []byte(strings.Join(h.summaryLines(), "\n")+"\n"))
		}
		renderHealth(h)
		logger.Info("✅ Health report generated")
		return nil
	}
}

func writeReportFile(path string, data []byte) error {
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("failed to write report to %s: %w", path, err)
	}
	logger.Info("✅ Health report written to " + path)
	return nil
}
