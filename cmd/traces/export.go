package traces

import (
	"context"
	"fmt"
	"os"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
)

var exportFile string

var exportCmd = &cobra.Command{
	Use:   "export",
	Short: "Export a trace",
	Long: `Export a single trace by ID from Tempo as JSON.

Fetches the trace from Tempo's /api/traces/{id} endpoint and writes the OTLP
JSON payload to stdout or --file.

Examples:
  adhar traces export --trace abc123
  adhar traces export --trace abc123 --file trace.json`,
	RunE: runExport,
}

func init() {
	exportCmd.Flags().StringVar(&exportFile, "file", "", "Output file (default stdout)")
}

func runExport(cmd *cobra.Command, args []string) error {
	if traceID == "" {
		return fmt.Errorf("--trace <id> is required to export a trace")
	}

	logger.Info(fmt.Sprintf("📤 Exporting trace %s from Tempo...", traceID))
	ctx := context.Background()

	body, err := getTrace(ctx, tempoURL, traceID)
	if err != nil {
		return err
	}

	if exportFile != "" {
		if err := os.WriteFile(exportFile, body, 0o644); err != nil {
			return fmt.Errorf("writing trace to file: %w", err)
		}
		fmt.Println(helpers.CreateSuccess(fmt.Sprintf("✅ Exported trace %s to %s", traceID, exportFile)))
		return nil
	}

	fmt.Println(string(body))
	return nil
}
