package metrics

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var dashboardCmd = &cobra.Command{
	Use:   "dashboard",
	Short: "List Grafana dashboards",
	Long: `List Grafana dashboards provisioned in the cluster.

Dashboards installed via the kube-prometheus-stack are stored as ConfigMaps
labeled "grafana_dashboard=1" and side-loaded by the Grafana sidecar. This
command lists those ConfigMaps and prints the Grafana URL for access.

Examples:
  adhar metrics dashboard
  adhar metrics dashboard --namespace monitoring
  adhar metrics dashboard --grafana-url http://localhost:3000`,
	RunE: runDashboard,
}

func runDashboard(cmd *cobra.Command, args []string) error {
	logger.Info("📊 Listing Grafana dashboards (ConfigMaps labeled grafana_dashboard)...")
	ctx := context.Background()

	clientset, err := getClientset()
	if err != nil {
		return err
	}

	ns := namespace // empty => all namespaces
	cms, err := clientset.CoreV1().ConfigMaps(ns).List(ctx, metav1.ListOptions{
		LabelSelector: "grafana_dashboard",
	})
	if err != nil {
		return fmt.Errorf("listing dashboard ConfigMaps: %w", err)
	}

	type dashRow struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
		Panels    int    `json:"dashboards"`
	}
	rows := make([]dashRow, 0, len(cms.Items))
	for _, cm := range cms.Items {
		rows = append(rows, dashRow{Name: cm.Name, Namespace: cm.Namespace, Panels: len(cm.Data)})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })

	switch output {
	case "json":
		return helpers.PrintJSON(rows)
	case "yaml":
		return helpers.PrintYAML(rows)
	}

	fmt.Println(helpers.CreateInfo("🌐 Grafana: " + grafanaURL))
	fmt.Println()

	if len(rows) == 0 {
		fmt.Println(helpers.CreateMuted("No dashboard ConfigMaps found. Open Grafana directly at the URL above."))
		return nil
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("%-45s %-18s %s\n", "📊 CONFIGMAP", "📦 NAMESPACE", "📁 FILES"))
	b.WriteString(strings.Repeat("─", 80) + "\n")
	for _, r := range rows {
		b.WriteString(fmt.Sprintf("%-45s %-18s %d\n", trunc(r.Name, 45), trunc(r.Namespace, 18), r.Panels))
	}
	fmt.Println(helpers.BorderStyle.Render(b.String()))
	fmt.Println(helpers.CreateMuted(fmt.Sprintf("%d dashboard ConfigMap(s)", len(rows))))
	return nil
}
