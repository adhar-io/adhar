package service

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

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List all services",
	Long: `List Kubernetes Services and their type, cluster IP, and ports.

Examples:
  adhar service list
  adhar service list --namespace=prod
  adhar service list --output=json`,
	RunE: runList,
}

func runList(cmd *cobra.Command, args []string) error {
	ns := resolveNamespace()
	logger.Info(fmt.Sprintf("📋 Listing services in namespace %s...", ns))

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

	if output == "json" {
		return helpers.PrintJSON(services.Items)
	}
	if output == "yaml" {
		return helpers.PrintYAML(services.Items)
	}

	fmt.Printf("\n%s\n", helpers.TitleStyle.Render("🌐 Services"))
	var t strings.Builder
	t.WriteString(fmt.Sprintf("%-32s %-14s %-18s %-25s\n", "NAME", "TYPE", "CLUSTER-IP", "PORTS"))
	t.WriteString(strings.Repeat("─", 92) + "\n")
	if len(services.Items) == 0 {
		t.WriteString("(none)\n")
	}
	for _, svc := range services.Items {
		t.WriteString(fmt.Sprintf("%-32s %-14s %-18s %-25s\n",
			svc.Name, string(svc.Spec.Type), clusterIP(svc), formatPorts(svc.Spec.Ports)))
	}
	fmt.Println(helpers.BorderStyle.Width(95).Render(t.String()))
	return nil
}

func clusterIP(svc corev1.Service) string {
	if svc.Spec.ClusterIP == "" {
		return "<none>"
	}
	return svc.Spec.ClusterIP
}

func formatPorts(ports []corev1.ServicePort) string {
	if len(ports) == 0 {
		return "<none>"
	}
	var parts []string
	for _, p := range ports {
		entry := fmt.Sprintf("%d", p.Port)
		if p.NodePort != 0 {
			entry = fmt.Sprintf("%d:%d", p.Port, p.NodePort)
		}
		entry += "/" + string(p.Protocol)
		parts = append(parts, entry)
	}
	return strings.Join(parts, ",")
}
