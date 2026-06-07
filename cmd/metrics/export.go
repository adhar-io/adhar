package metrics

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
)

var exportFormat string
var exportFile string

var exportCmd = &cobra.Command{
	Use:   "export",
	Short: "Export metrics data",
	Long: `Export the result of a PromQL query for analysis.

Runs an instant query against Prometheus and writes the resulting series to
stdout (or --file) as JSON or CSV.

Examples:
  adhar metrics export --query 'up' --format csv
  adhar metrics export --query 'sum(rate(http_requests_total[5m]))' --format json --file out.json`,
	RunE: runExport,
}

func init() {
	exportCmd.Flags().StringVar(&exportFormat, "format", "json", "Export format (json, csv)")
	exportCmd.Flags().StringVar(&exportFile, "file", "", "Output file (default stdout)")
}

func runExport(cmd *cobra.Command, args []string) error {
	logger.Info(fmt.Sprintf("📤 Exporting Prometheus query: %s", promQueryExpr))
	ctx := context.Background()

	data, err := promQuery(ctx, prometheusURL, promQueryExpr)
	if err != nil {
		return err
	}

	var result promVectorResult
	if err := json.Unmarshal(data, &result); err != nil {
		return fmt.Errorf("decoding query result: %w", err)
	}

	out := os.Stdout
	if exportFile != "" {
		f, ferr := os.Create(exportFile)
		if ferr != nil {
			return fmt.Errorf("creating output file: %w", ferr)
		}
		defer f.Close()
		out = f
	}

	switch exportFormat {
	case "csv":
		w := csv.NewWriter(out)
		defer w.Flush()
		if err := w.Write([]string{"series", "value"}); err != nil {
			return err
		}
		for _, s := range result.Result {
			val := ""
			if len(s.Value) == 2 {
				val = fmt.Sprintf("%v", s.Value[1])
			}
			if err := w.Write([]string{seriesLabel(s.Metric), val}); err != nil {
				return err
			}
		}
	default:
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(result); err != nil {
			return err
		}
	}

	if exportFile != "" {
		fmt.Println(helpers.CreateSuccess(fmt.Sprintf("✅ Exported %d series to %s", len(result.Result), exportFile)))
	}
	return nil
}
