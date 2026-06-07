package service

import (
	"context"
	"fmt"
	"strings"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var testCmd = &cobra.Command{
	Use:     "test",
	Aliases: []string{"status"},
	Short:   "Inspect a service and its endpoints",
	Long: `Describe a Service and its backing Endpoints to verify it has ready
addresses. A service with zero ready endpoints will not receive traffic.

Examples:
  adhar service test --name=api
  adhar service test --name=web --namespace=prod`,
	RunE: runTest,
}

func runTest(cmd *cobra.Command, args []string) error {
	if serviceName == "" {
		return fmt.Errorf("--name is required for service testing")
	}

	ns := resolveNamespace()
	logger.Info(fmt.Sprintf("🧪 Inspecting service %s/%s...", ns, serviceName))

	clientset, err := getClientset()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), parseTimeout())
	defer cancel()

	svc, err := clientset.CoreV1().Services(ns).Get(ctx, serviceName, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return fmt.Errorf("service %s/%s not found", ns, serviceName)
		}
		return fmt.Errorf("getting service %s/%s: %w", ns, serviceName, err)
	}

	endpoints, err := clientset.CoreV1().Endpoints(ns).Get(ctx, serviceName, metav1.GetOptions{})
	if err != nil && !k8serrors.IsNotFound(err) {
		return fmt.Errorf("getting endpoints %s/%s: %w", ns, serviceName, err)
	}

	ready, notReady := countEndpoints(endpoints)

	if output == "json" {
		return helpers.PrintJSON(map[string]interface{}{
			"service":           svc,
			"readyAddresses":    ready,
			"notReadyAddresses": notReady,
		})
	}
	if output == "yaml" {
		return helpers.PrintYAML(map[string]interface{}{
			"service":           svc,
			"readyAddresses":    ready,
			"notReadyAddresses": notReady,
		})
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("🏷️  Service:    %s/%s\n", ns, svc.Name))
	b.WriteString(fmt.Sprintf("📦 Type:       %s\n", svc.Spec.Type))
	b.WriteString(fmt.Sprintf("🔌 ClusterIP:  %s\n", clusterIP(*svc)))
	b.WriteString(fmt.Sprintf("🚪 Ports:      %s\n", formatPorts(svc.Spec.Ports)))
	b.WriteString(fmt.Sprintf("🎯 Selector:   %s\n", formatSelector(svc.Spec.Selector)))
	b.WriteString(fmt.Sprintf("✅ Ready:      %d address(es)\n", ready))
	b.WriteString(fmt.Sprintf("⚠️  Not Ready:  %d address(es)", notReady))
	fmt.Println(helpers.BorderStyle.Width(70).Render(b.String()))

	if ready == 0 {
		fmt.Println(helpers.CreateWarning("⚠️  No ready endpoints — this service will not serve traffic."))
	} else {
		fmt.Println(helpers.CreateSuccess("✅ Service has ready endpoints."))
	}
	return nil
}

func countEndpoints(ep *corev1.Endpoints) (ready, notReady int) {
	if ep == nil {
		return 0, 0
	}
	for _, subset := range ep.Subsets {
		ready += len(subset.Addresses)
		notReady += len(subset.NotReadyAddresses)
	}
	return ready, notReady
}

func formatSelector(sel map[string]string) string {
	if len(sel) == 0 {
		return "<none>"
	}
	var parts []string
	for k, v := range sel {
		parts = append(parts, fmt.Sprintf("%s=%s", k, v))
	}
	return strings.Join(parts, ",")
}
