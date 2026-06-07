package webhook

import (
	"context"
	"fmt"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var createCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a validating admission webhook",
	Long: `Create a ValidatingWebhookConfiguration backed by an external URL.

This registers a URL-backed validating admission webhook (the simplest form that
requires no in-cluster Service or CA bundle, relying on the endpoint's own TLS).
It scopes to CREATE of the given --resource so it is easy to remove afterwards.

Examples:
  adhar webhook create --name=my-validator --url=https://validator.example.com/validate
  adhar webhook create --name=cm-guard --url=https://guard.internal/admit --resource=configmaps`,
	RunE: runCreate,
}

var createResource string

func init() {
	createCmd.Flags().StringVar(&createResource, "resource", "pods", "Resource the webhook validates on CREATE")
}

func runCreate(cmd *cobra.Command, args []string) error {
	if webhookName == "" {
		return fmt.Errorf("--name is required for webhook creation")
	}
	if webhookURL == "" {
		return fmt.Errorf("--url is required (URL-backed webhook endpoint, https://...)")
	}

	logger.Info(fmt.Sprintf("🔗 Creating ValidatingWebhookConfiguration %q -> %s", webhookName, webhookURL))
	ctx := context.Background()

	cs, err := getClientset()
	if err != nil {
		return err
	}

	sideEffects := admissionv1.SideEffectClassNone
	failPolicy := admissionv1.Ignore // Ignore so a broken endpoint cannot wedge the cluster.
	url := webhookURL

	cfg := &admissionv1.ValidatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name:   webhookName,
			Labels: map[string]string{"app.kubernetes.io/managed-by": "adhar"},
		},
		Webhooks: []admissionv1.ValidatingWebhook{
			{
				Name: webhookName + ".adhar.io",
				ClientConfig: admissionv1.WebhookClientConfig{
					URL: &url,
				},
				Rules: []admissionv1.RuleWithOperations{
					{
						Operations: []admissionv1.OperationType{admissionv1.Create},
						Rule: admissionv1.Rule{
							APIGroups:   []string{"*"},
							APIVersions: []string{"*"},
							Resources:   []string{createResource},
						},
					},
				},
				FailurePolicy:           &failPolicy,
				SideEffects:             &sideEffects,
				AdmissionReviewVersions: []string{"v1"},
			},
		},
	}

	created, err := cs.AdmissionregistrationV1().ValidatingWebhookConfigurations().Create(ctx, cfg, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("creating ValidatingWebhookConfiguration: %w", err)
	}

	fmt.Println(helpers.CreateSuccess(fmt.Sprintf("✅ Created ValidatingWebhookConfiguration %q (failurePolicy=Ignore)", created.Name)))
	fmt.Println(helpers.CreateMuted("   Remove with: kubectl delete validatingwebhookconfiguration " + created.Name))
	return nil
}
