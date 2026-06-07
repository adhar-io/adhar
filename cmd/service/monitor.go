package service

import (
	"context"
	"fmt"
	"strings"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var monitorCmd = &cobra.Command{
	Use:   "monitor",
	Short: "Summarize service endpoint readiness",
	Long: `Show every Service in a namespace alongside the number of ready and
not-ready backing endpoint addresses. This is a read-only snapshot.

Examples:
  adhar service monitor
  adhar service monitor --namespace=prod`,
	RunE: runMonitor,
}

func runMonitor(cmd *cobra.Command, args []string) error {
	ns := resolveNamespace()
	logger.Info(fmt.Sprintf("📊 Monitoring services in namespace %s...", ns))

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

	type row struct {
		Name     string `json:"name"`
		Type     string `json:"type"`
		Ready    int    `json:"ready"`
		NotReady int    `json:"notReady"`
	}
	var rows []row
	for _, svc := range services.Items {
		ep, epErr := clientset.CoreV1().Endpoints(ns).Get(ctx, svc.Name, metav1.GetOptions{})
		ready, notReady := 0, 0
		if epErr == nil {
			ready, notReady = countEndpoints(ep)
		}
		rows = append(rows, row{svc.Name, string(svc.Spec.Type), ready, notReady})
	}

	if output == "json" {
		return helpers.PrintJSON(rows)
	}
	if output == "yaml" {
		return helpers.PrintYAML(rows)
	}

	fmt.Printf("\n%s\n", helpers.TitleStyle.Render("📊 Service Endpoint Health"))
	var t strings.Builder
	t.WriteString(fmt.Sprintf("%-32s %-14s %-9s %-9s\n", "NAME", "TYPE", "READY", "NOTREADY"))
	t.WriteString(strings.Repeat("─", 70) + "\n")
	if len(rows) == 0 {
		t.WriteString("(none)\n")
	}
	for _, r := range rows {
		t.WriteString(fmt.Sprintf("%-32s %-14s %-9d %-9d\n", r.Name, r.Type, r.Ready, r.NotReady))
	}
	fmt.Println(helpers.BorderStyle.Width(75).Render(t.String()))
	return nil
}
