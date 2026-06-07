package webhook

import (
	"context"
	"fmt"
	"strings"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var securityCmd = &cobra.Command{
	Use:   "security",
	Short: "Report webhook security posture",
	Long: `Report the TLS/security posture of admission webhooks.

For each webhook this shows whether a CA bundle is configured, the failure
policy, and the side-effect class — the key security-relevant settings of an
admission webhook.

Examples:
  adhar webhook security
  adhar webhook security --name cert-manager`,
	RunE: runSecurity,
}

func runSecurity(cmd *cobra.Command, args []string) error {
	logger.Info("🔐 Reporting admission webhook security posture...")
	ctx := context.Background()

	cs, err := getClientset()
	if err != nil {
		return err
	}

	type secRow struct {
		Config      string `json:"config"`
		Kind        string `json:"kind"`
		Webhook     string `json:"webhook"`
		CABundle    string `json:"caBundle"`
		FailPolicy  string `json:"failurePolicy"`
		SideEffects string `json:"sideEffects"`
	}

	caState := func(cc admissionv1.WebhookClientConfig) string {
		if len(cc.CABundle) > 0 {
			return "✅ present"
		}
		if cc.URL != nil {
			return "🌐 url-tls"
		}
		return "❌ missing"
	}
	sideEffects := func(se *admissionv1.SideEffectClass) string {
		if se == nil {
			return "Unknown"
		}
		return string(*se)
	}

	var rows []secRow

	vwcs, err := cs.AdmissionregistrationV1().ValidatingWebhookConfigurations().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing validating webhook configurations: %w", err)
	}
	for _, c := range vwcs.Items {
		if !nameMatches(c.Name, webhookName) {
			continue
		}
		for _, w := range c.Webhooks {
			rows = append(rows, secRow{c.Name, "Validating", w.Name, caState(w.ClientConfig), failurePolicy(w.FailurePolicy), sideEffects(w.SideEffects)})
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
			rows = append(rows, secRow{c.Name, "Mutating", w.Name, caState(w.ClientConfig), failurePolicy(w.FailurePolicy), sideEffects(w.SideEffects)})
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
	b.WriteString(fmt.Sprintf("%-26s %-11s %-30s %-12s %-10s %s\n", "⚙️  CONFIG", "🔖 KIND", "🪝 WEBHOOK", "🔐 CA", "🛡️  FAIL", "♻️  SIDE-FX"))
	b.WriteString(strings.Repeat("─", 110) + "\n")
	for _, r := range rows {
		b.WriteString(fmt.Sprintf("%-26s %-11s %-30s %-12s %-10s %s\n",
			trunc(r.Config, 26), r.Kind, trunc(r.Webhook, 30), r.CABundle, r.FailPolicy, r.SideEffects))
	}
	fmt.Println(helpers.BorderStyle.Render(b.String()))
	return nil
}
