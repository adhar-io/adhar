package traces

import (
	"context"
	"fmt"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List recent traces",
	Long: `List recent traces from Tempo.

This performs an unfiltered Tempo search and prints the most recent traces.
Use --service to scope to a single service, or 'adhar traces search' for richer
filtering.

Examples:
  adhar traces list
  adhar traces list --service=web --limit 50
  adhar traces list --tempo-url http://localhost:3100`,
	RunE: runList,
}

func runList(cmd *cobra.Command, args []string) error {
	logger.Info("📋 Listing recent traces from Tempo...")
	ctx := context.Background()

	res, err := searchTraces(ctx, tempoURL, service, operation, tags, traceLimit)
	if err != nil {
		return err
	}

	switch output {
	case "json":
		return helpers.PrintJSON(res.Traces)
	case "yaml":
		return helpers.PrintYAML(res.Traces)
	}

	if len(res.Traces) == 0 {
		fmt.Println(helpers.CreateMuted("No traces found. Is Tempo receiving spans?"))
		return nil
	}

	fmt.Println(helpers.BorderStyle.Render(renderTraceTable(res.Traces)))
	fmt.Println(helpers.CreateMuted(fmt.Sprintf("%d trace(s)", len(res.Traces))))
	return nil
}
