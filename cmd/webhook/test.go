package webhook

import (
	"context"
	"fmt"
	"time"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var testCmd = &cobra.Command{
	Use:   "test",
	Short: "Test admission webhooks via a dry-run",
	Long: `Test that admission webhooks respond by issuing a server-side dry-run apply.

A throwaway ConfigMap is created with dryRun=All in the target namespace. This
exercises the admission chain without persisting anything: if a webhook is
unreachable or rejects the request, the error is reported here.

Examples:
  adhar webhook test
  adhar webhook test --namespace kyverno`,
	RunE: runTest,
}

func runTest(cmd *cobra.Command, args []string) error {
	ns := namespace
	if ns == "" {
		ns = globals.AdharSystemNamespace
	}

	logger.Info(fmt.Sprintf("🧪 Issuing dry-run admission test in namespace %q...", ns))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cs, err := getClientset()
	if err != nil {
		return err
	}

	probe := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "adhar-webhook-probe-",
			Namespace:    ns,
			Labels:       map[string]string{"adhar.io/webhook-probe": "true"},
		},
		Data: map[string]string{"probe": "dry-run"},
	}

	_, err = cs.CoreV1().ConfigMaps(ns).Create(ctx, probe, metav1.CreateOptions{
		DryRun: []string{metav1.DryRunAll},
	})
	if err != nil {
		// A webhook rejection is a real, useful result — surface it as an error.
		fmt.Println(helpers.CreateWarning("⚠️  Admission request was rejected or a webhook failed:"))
		return fmt.Errorf("dry-run admission failed: %w", err)
	}

	fmt.Println(helpers.CreateSuccess("✅ Dry-run admission succeeded — the admission webhook chain responded without rejecting the request."))
	return nil
}
