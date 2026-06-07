package webhook

import (
	"context"
	"fmt"
	"strings"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List admission webhooks",
	Long: `List Kubernetes admission webhooks (Validating/Mutating WebhookConfigurations).

These are the cluster's admission control webhooks (e.g. cert-manager, kyverno).
Filter to a single configuration by name with --name.

Examples:
  adhar webhook list
  adhar webhook list --name kyverno
  adhar webhook list --output json`,
	RunE: runList,
}

func runList(cmd *cobra.Command, args []string) error {
	logger.Info("📋 Listing admission webhooks...")
	ctx := context.Background()

	cs, err := getClientset()
	if err != nil {
		return err
	}

	rows, err := collectWebhooks(ctx, cs)
	if err != nil {
		return err
	}
	rows = filterByName(rows, webhookName)

	switch output {
	case "json":
		return helpers.PrintJSON(rows)
	case "yaml":
		return helpers.PrintYAML(rows)
	}

	if len(rows) == 0 {
		fmt.Println(helpers.CreateMuted("No admission webhooks found."))
		return nil
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("%-26s %-11s %-30s %-26s %s\n", "⚙️  CONFIG", "🔖 KIND", "🪝 WEBHOOK", "📡 TARGET", "🛡️  FAIL"))
	b.WriteString(strings.Repeat("─", 110) + "\n")
	for _, r := range rows {
		b.WriteString(fmt.Sprintf("%-26s %-11s %-30s %-26s %s\n",
			trunc(r.Config, 26), r.Kind, trunc(r.Webhook, 30), trunc(r.Service, 26), r.FailurePolicy))
	}
	fmt.Println(helpers.BorderStyle.Render(b.String()))
	fmt.Println(helpers.CreateMuted(fmt.Sprintf("%d webhook(s)", len(rows))))
	return nil
}

// filterByName keeps rows whose config name contains the filter (case-insensitive).
func filterByName(rows []webhookRow, filter string) []webhookRow {
	if filter == "" {
		return rows
	}
	f := strings.ToLower(filter)
	var out []webhookRow
	for _, r := range rows {
		if strings.Contains(strings.ToLower(r.Config), f) {
			out = append(out, r)
		}
	}
	return out
}
