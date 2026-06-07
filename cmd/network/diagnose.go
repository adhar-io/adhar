package network

import (
	"context"
	"fmt"
	"strings"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var diagnoseCmd = &cobra.Command{
	Use:     "diagnose",
	Aliases: []string{"list"},
	Short:   "List network resources and CNI status",
	Long: `List the network resources in a namespace — Services and
NetworkPolicies — alongside best-effort Cilium CNI status.

Examples:
  adhar network diagnose
  adhar network diagnose --service=web
  adhar network diagnose --namespace=prod`,
	RunE: runDiagnose,
}

func runDiagnose(cmd *cobra.Command, args []string) error {
	ns := resolveNamespace()
	logger.Info(fmt.Sprintf("🔍 Inspecting network resources in namespace %s...", ns))

	clientset, err := getClientset()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), parseTimeout())
	defer cancel()

	services, err := clientset.CoreV1().Services(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing services in %s: %w", ns, err)
	}
	policies, err := clientset.NetworkingV1().NetworkPolicies(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing network policies in %s: %w", ns, err)
	}
	cilium := ciliumStatus(ctx, clientset)

	if output == "json" {
		return helpers.PrintJSON(map[string]interface{}{
			"services":        services.Items,
			"networkPolicies": policies.Items,
			"cilium":          cilium,
		})
	}
	if output == "yaml" {
		return helpers.PrintYAML(map[string]interface{}{
			"services":        services.Items,
			"networkPolicies": policies.Items,
			"cilium":          cilium,
		})
	}

	fmt.Println(helpers.BorderStyle.Width(70).Render("🕸️  CNI: " + cilium))

	fmt.Printf("\n%s\n", helpers.TitleStyle.Render("🌐 Services"))
	var st strings.Builder
	st.WriteString(fmt.Sprintf("%-32s %-14s %-25s\n", "NAME", "TYPE", "PORTS"))
	st.WriteString(strings.Repeat("─", 72) + "\n")
	if len(services.Items) == 0 {
		st.WriteString("(none)\n")
	}
	for _, svc := range services.Items {
		st.WriteString(fmt.Sprintf("%-32s %-14s %-25s\n", svc.Name, string(svc.Spec.Type), svcPorts(svc)))
	}
	fmt.Println(helpers.BorderStyle.Width(75).Render(st.String()))

	fmt.Printf("\n%s\n", helpers.TitleStyle.Render("🛡️  Network Policies"))
	var pt strings.Builder
	pt.WriteString(fmt.Sprintf("%-32s %-30s\n", "NAME", "POD SELECTOR"))
	pt.WriteString(strings.Repeat("─", 65) + "\n")
	if len(policies.Items) == 0 {
		pt.WriteString("(none)\n")
	}
	for _, p := range policies.Items {
		pt.WriteString(fmt.Sprintf("%-32s %-30s\n", p.Name, selectorString(p.Spec.PodSelector.MatchLabels)))
	}
	fmt.Println(helpers.BorderStyle.Width(70).Render(pt.String()))

	return nil
}

func svcPorts(svc corev1.Service) string {
	if len(svc.Spec.Ports) == 0 {
		return "<none>"
	}
	var parts []string
	for _, p := range svc.Spec.Ports {
		parts = append(parts, fmt.Sprintf("%d/%s", p.Port, p.Protocol))
	}
	return strings.Join(parts, ",")
}

func selectorString(m map[string]string) string {
	if len(m) == 0 {
		return "<all pods>"
	}
	var parts []string
	for k, v := range m {
		parts = append(parts, fmt.Sprintf("%s=%s", k, v))
	}
	return strings.Join(parts, ",")
}
