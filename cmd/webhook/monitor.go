package webhook

import (
	"context"
	"fmt"
	"strings"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

var monitorCmd = &cobra.Command{
	Use:   "monitor",
	Short: "Monitor webhook backend health",
	Long: `Monitor admission webhook health by checking their backing services.

For each webhook backed by an in-cluster Service, this resolves the Service's
Endpoints and reports whether ready backends exist (i.e. the webhook can be
served). Webhooks with an external URL backend are reported as such.

Examples:
  adhar webhook monitor
  adhar webhook monitor --name cert-manager`,
	RunE: runMonitor,
}

func runMonitor(cmd *cobra.Command, args []string) error {
	logger.Info("📊 Checking admission webhook backend health...")
	ctx := context.Background()

	cs, err := getClientset()
	if err != nil {
		return err
	}

	type healthRow struct {
		Config  string `json:"config"`
		Kind    string `json:"kind"`
		Webhook string `json:"webhook"`
		Backend string `json:"backend"`
		Health  string `json:"health"`
	}

	var rows []healthRow
	addService := func(config, kind, name string, cc admissionv1.WebhookClientConfig) {
		row := healthRow{Config: config, Kind: kind, Webhook: name, Backend: clientService(cc)}
		switch {
		case cc.URL != nil:
			row.Health = "🌐 external URL"
		case cc.Service != nil:
			row.Health = serviceHealth(ctx, cs, cc.Service.Namespace, cc.Service.Name)
		default:
			row.Health = "❓ unknown"
		}
		rows = append(rows, row)
	}

	vwcs, err := cs.AdmissionregistrationV1().ValidatingWebhookConfigurations().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing validating webhook configurations: %w", err)
	}
	for _, c := range vwcs.Items {
		if !nameMatches(c.Name, webhookName) {
			continue
		}
		for _, w := range c.Webhooks {
			addService(c.Name, "Validating", w.Name, w.ClientConfig)
		}
	}

	mwcs, err := cs.AdmissionregistrationV1().MutatingWebhookConfigurations().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing mutating webhook configurations: %w", err)
	}
	for _, c := range mwcs.Items {
		if !nameMatches(c.Name, webhookName) {
			continue
		}
		for _, w := range c.Webhooks {
			addService(c.Name, "Mutating", w.Name, w.ClientConfig)
		}
	}

	switch output {
	case "json":
		return helpers.PrintJSON(rows)
	case "yaml":
		return helpers.PrintYAML(rows)
	}

	if len(rows) == 0 {
		fmt.Println(helpers.CreateMuted("No matching admission webhooks found."))
		return nil
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("%-26s %-11s %-30s %s\n", "⚙️  CONFIG", "🔖 KIND", "🪝 WEBHOOK", "❤️  HEALTH"))
	b.WriteString(strings.Repeat("─", 95) + "\n")
	for _, r := range rows {
		b.WriteString(fmt.Sprintf("%-26s %-11s %-30s %s\n", trunc(r.Config, 26), r.Kind, trunc(r.Webhook, 30), r.Health))
	}
	fmt.Println(helpers.BorderStyle.Render(b.String()))
	return nil
}

func nameMatches(config, filter string) bool {
	if filter == "" {
		return true
	}
	return strings.Contains(strings.ToLower(config), strings.ToLower(filter))
}

// serviceHealth reports whether a backing Service has ready endpoints.
func serviceHealth(ctx context.Context, cs *kubernetes.Clientset, ns, name string) string {
	ep, err := cs.CoreV1().Endpoints(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return "❌ service missing"
		}
		return "⚠️ " + err.Error()
	}
	ready := 0
	for _, subset := range ep.Subsets {
		ready += len(subset.Addresses)
	}
	if ready == 0 {
		return "⚠️ no ready endpoints"
	}
	return fmt.Sprintf("✅ %d endpoint(s)", ready)
}
