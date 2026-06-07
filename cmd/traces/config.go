package traces

import (
	"context"
	"fmt"
	"strings"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Show effective tracing configuration",
	Long: `Show the effective Tempo endpoint and probe its readiness.

Prints the resolved --tempo-url and queries Tempo's /ready endpoint so you can
confirm connectivity before running searches.

Examples:
  adhar traces config
  adhar traces config --tempo-url http://localhost:3100`,
	RunE: runConfig,
}

func runConfig(cmd *cobra.Command, args []string) error {
	logger.Info("⚙️  Effective tracing configuration")
	ctx := context.Background()

	var b strings.Builder
	b.WriteString(fmt.Sprintf("🔗 Tempo URL:  %s\n", tempoURL))
	b.WriteString(fmt.Sprintf("⏱️  Timeout:    %s\n", httpTimeout()))
	b.WriteString(fmt.Sprintf("🔢 Limit:      %d\n", traceLimit))

	// Probe readiness (best-effort).
	status := "✅ ready"
	if _, err := tempoGet(ctx, tempoURL, "/ready", ""); err != nil {
		status = "❌ unreachable (" + err.Error() + ")"
	}
	b.WriteString(fmt.Sprintf("📡 Status:     %s", status))

	fmt.Println(helpers.BorderStyle.Render(b.String()))
	return nil
}
