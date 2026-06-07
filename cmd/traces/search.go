package traces

import (
	"context"
	"fmt"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
)

var searchCmd = &cobra.Command{
	Use:   "search",
	Short: "Search traces",
	Long: `Search traces in Tempo by service, operation, and tags.

Queries the Tempo /api/search endpoint. Filters are combined: --service maps to
the service.name tag, --operation to the span name, and --tags appends extra
TraceQL tag filters.

Examples:
  adhar traces search --service=web
  adhar traces search --service=web --operation=GET
  adhar traces search --service=web --tags 'http.status_code=500'`,
	RunE: runSearch,
}

func runSearch(cmd *cobra.Command, args []string) error {
	if service == "" && operation == "" && tags == "" {
		return fmt.Errorf("provide at least one filter: --service, --operation, or --tags")
	}

	logger.Info("🔍 Searching traces in Tempo...")
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
		fmt.Println(helpers.CreateMuted("No matching traces found."))
		return nil
	}

	fmt.Println(helpers.BorderStyle.Render(renderTraceTable(res.Traces)))
	fmt.Println(helpers.CreateMuted(fmt.Sprintf("%d matching trace(s) (inspected %d)", len(res.Traces), res.Metrics.InspectedTraces)))
	return nil
}
