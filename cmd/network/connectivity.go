package network

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
	"k8s.io/client-go/kubernetes"
)

var connectivityCmd = &cobra.Command{
	Use:     "connectivity",
	Aliases: []string{"status"},
	Short:   "Summarize cluster connectivity",
	Long: `Summarize network connectivity readiness: node readiness, Cilium CNI
status, and per-service endpoint readiness. With --from/--to, reports whether
both named services currently have ready endpoints.

Examples:
  adhar network connectivity
  adhar network connectivity --from=web --to=api
  adhar network connectivity --namespace=prod`,
	RunE: runConnectivity,
}

var (
	fromService string
	toService   string
)

func init() {
	connectivityCmd.Flags().StringVarP(&fromService, "from", "f", "", "Source service")
	connectivityCmd.Flags().StringVarP(&toService, "to", "t", "", "Target service")
}

func runConnectivity(cmd *cobra.Command, args []string) error {
	logger.Info("🔗 Summarizing network connectivity...")

	clientset, err := getClientset()
	if err != nil {
		return err
	}

	if fromService != "" && toService != "" {
		return reportServicePair(clientset, fromService, toService)
	}
	return connectivitySummary(clientset)
}

// reportServicePair checks that both named services have ready endpoints.
func reportServicePair(clientset *kubernetes.Clientset, from, to string) error {
	ns := resolveNamespace()
	ctx, cancel := context.WithTimeout(context.Background(), parseTimeout())
	defer cancel()

	fromReady, err := serviceReady(ctx, clientset, ns, from)
	if err != nil {
		return err
	}
	toReady, err := serviceReady(ctx, clientset, ns, to)
	if err != nil {
		return err
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("🔗 Namespace: %s\n", ns))
	b.WriteString(fmt.Sprintf("📤 %-20s %s\n", from, readyLabel(fromReady)))
	b.WriteString(fmt.Sprintf("📥 %-20s %s", to, readyLabel(toReady)))
	fmt.Println(helpers.BorderStyle.Width(60).Render(b.String()))

	if fromReady && toReady {
		fmt.Println(helpers.CreateSuccess("✅ Both services have ready endpoints."))
	} else {
		fmt.Println(helpers.CreateWarning("⚠️  One or both services have no ready endpoints."))
	}
	return nil
}

func serviceReady(ctx context.Context, clientset *kubernetes.Clientset, ns, name string) (bool, error) {
	if _, err := clientset.CoreV1().Services(ns).Get(ctx, name, metav1.GetOptions{}); err != nil {
		if k8serrors.IsNotFound(err) {
			return false, fmt.Errorf("service %s/%s not found", ns, name)
		}
		return false, fmt.Errorf("getting service %s/%s: %w", ns, name, err)
	}
	ep, err := clientset.CoreV1().Endpoints(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("getting endpoints %s/%s: %w", ns, name, err)
	}
	for _, s := range ep.Subsets {
		if len(s.Addresses) > 0 {
			return true, nil
		}
	}
	return false, nil
}

func readyLabel(ready bool) string {
	if ready {
		return "✅ ready endpoints"
	}
	return "⚠️  no ready endpoints"
}

// connectivitySummary reports node readiness, Cilium status, and per-service
// endpoint readiness in the namespace.
func connectivitySummary(clientset *kubernetes.Clientset) error {
	ns := resolveNamespace()
	ctx, cancel := context.WithTimeout(context.Background(), parseTimeout())
	defer cancel()

	nodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing nodes: %w", err)
	}
	nodesReady := 0
	for _, n := range nodes.Items {
		for _, c := range n.Status.Conditions {
			if c.Type == corev1.NodeReady && c.Status == corev1.ConditionTrue {
				nodesReady++
				break
			}
		}
	}

	services, err := clientset.CoreV1().Services(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing services in %s: %w", ns, err)
	}
	withEndpoints, withoutEndpoints := 0, 0
	for _, svc := range services.Items {
		ready, _ := serviceReady(ctx, clientset, ns, svc.Name)
		if ready {
			withEndpoints++
		} else {
			withoutEndpoints++
		}
	}

	cilium := ciliumStatus(ctx, clientset)

	if output == "json" {
		return helpers.PrintJSON(map[string]interface{}{
			"namespace":             ns,
			"nodesReady":            fmt.Sprintf("%d/%d", nodesReady, len(nodes.Items)),
			"cilium":                cilium,
			"servicesWithEndpoints": withEndpoints,
			"servicesNoEndpoints":   withoutEndpoints,
		})
	}
	if output == "yaml" {
		return helpers.PrintYAML(map[string]interface{}{
			"namespace":             ns,
			"nodesReady":            fmt.Sprintf("%d/%d", nodesReady, len(nodes.Items)),
			"cilium":                cilium,
			"servicesWithEndpoints": withEndpoints,
			"servicesNoEndpoints":   withoutEndpoints,
		})
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("🖥️  Nodes Ready:           %d/%d\n", nodesReady, len(nodes.Items)))
	b.WriteString(fmt.Sprintf("🕸️  CNI:                   %s\n", cilium))
	b.WriteString(fmt.Sprintf("🌐 Namespace:             %s\n", ns))
	b.WriteString(fmt.Sprintf("✅ Services w/ endpoints: %d\n", withEndpoints))
	b.WriteString(fmt.Sprintf("⚠️  Services w/o endpoints: %d", withoutEndpoints))
	fmt.Println(helpers.BorderStyle.Width(60).Render(b.String()))
	return nil
}
