package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List all metrics",
	Long: `List metric collection targets (Prometheus Operator ServiceMonitors).

By default this lists the ServiceMonitors configured in the cluster, which
define what Prometheus scrapes. Pass --query to instead run a PromQL instant
query against Prometheus and print the resulting series.

Examples:
  adhar metrics list
  adhar metrics list --namespace=monitoring
  adhar metrics list --query 'up' --prometheus-url http://localhost:9090`,
	RunE: runList,
}

func runList(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	// If the user supplied an explicit PromQL query, run it against Prometheus.
	if cmd.Flags().Changed("query") {
		return listFromPromQL(ctx)
	}

	logger.Info("📋 Listing metric collection targets (ServiceMonitors)...")
	return listServiceMonitors(ctx)
}

type serviceMonitorRow struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Endpoints int    `json:"endpoints"`
	Selector  string `json:"selector"`
}

// listServiceMonitors lists monitoring.coreos.com/v1 ServiceMonitors via the
// dynamic client.
func listServiceMonitors(ctx context.Context) error {
	dyn, err := getDynamicClient()
	if err != nil {
		return err
	}

	var res *unstructured.UnstructuredList
	if namespace != "" {
		res, err = dyn.Resource(serviceMonitorsGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	} else {
		res, err = dyn.Resource(serviceMonitorsGVR).List(ctx, metav1.ListOptions{})
	}
	if err != nil {
		return friendlyCRDError("ServiceMonitor", err)
	}

	rows := make([]serviceMonitorRow, 0, len(res.Items))
	for i := range res.Items {
		rows = append(rows, toServiceMonitorRow(&res.Items[i]))
	}

	switch output {
	case "json":
		return helpers.PrintJSON(rows)
	case "yaml":
		return helpers.PrintYAML(rows)
	}

	if len(rows) == 0 {
		fmt.Println(helpers.CreateMuted("No ServiceMonitors found. Is kube-prometheus-stack installed?"))
		return nil
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("%-30s %-18s %-6s %s\n", "📡 NAME", "📦 NAMESPACE", "🎯 EPS", "🏷️  SELECTOR"))
	b.WriteString(strings.Repeat("─", 90) + "\n")
	for _, m := range rows {
		b.WriteString(fmt.Sprintf("%-30s %-18s %-6d %s\n", trunc(m.Name, 30), trunc(m.Namespace, 18), m.Endpoints, m.Selector))
	}
	fmt.Println(helpers.BorderStyle.Render(b.String()))
	fmt.Println(helpers.CreateMuted(fmt.Sprintf("%d ServiceMonitor(s)", len(rows))))
	return nil
}

func toServiceMonitorRow(u *unstructured.Unstructured) serviceMonitorRow {
	row := serviceMonitorRow{
		Name:      u.GetName(),
		Namespace: u.GetNamespace(),
	}
	if eps, ok, _ := unstructured.NestedSlice(u.Object, "spec", "endpoints"); ok {
		row.Endpoints = len(eps)
	}
	if sel, ok, _ := unstructured.NestedStringMap(u.Object, "spec", "selector", "matchLabels"); ok {
		parts := make([]string, 0, len(sel))
		for k, v := range sel {
			parts = append(parts, fmt.Sprintf("%s=%s", k, v))
		}
		sort.Strings(parts)
		row.Selector = strings.Join(parts, ",")
	}
	return row
}

// listFromPromQL runs the --query expression against Prometheus and prints the
// returned series.
func listFromPromQL(ctx context.Context) error {
	logger.Info(fmt.Sprintf("📋 Querying Prometheus: %s", promQueryExpr))
	data, err := promQuery(ctx, prometheusURL, promQueryExpr)
	if err != nil {
		return err
	}

	var result promVectorResult
	if err := json.Unmarshal(data, &result); err != nil || result.ResultType == "" {
		// Fall back to raw output for non-vector result types.
		fmt.Println(string(data))
		return nil
	}

	if output == "json" {
		return helpers.PrintJSON(result)
	}

	if len(result.Result) == 0 {
		fmt.Println(helpers.CreateMuted("Query returned no series."))
		return nil
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("%-55s %s\n", "📈 SERIES", "📊 VALUE"))
	b.WriteString(strings.Repeat("─", 75) + "\n")
	for _, s := range result.Result {
		val := ""
		if len(s.Value) == 2 {
			val = fmt.Sprintf("%v", s.Value[1])
		}
		b.WriteString(fmt.Sprintf("%-55s %s\n", trunc(seriesLabel(s.Metric), 55), val))
	}
	fmt.Println(helpers.BorderStyle.Render(b.String()))
	return nil
}

// promSample is a single Prometheus instant-vector sample.
type promSample struct {
	Metric map[string]string `json:"metric"`
	Value  []interface{}     `json:"value"`
}

// promVectorResult models the "vector" result type of a Prometheus instant query.
type promVectorResult struct {
	ResultType string       `json:"resultType"`
	Result     []promSample `json:"result"`
}

// seriesLabel renders a Prometheus metric label set as name{k="v",...}.
func seriesLabel(m map[string]string) string {
	name := m["__name__"]
	var parts []string
	for k, v := range m {
		if k == "__name__" {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%q", k, v))
	}
	sort.Strings(parts)
	if len(parts) == 0 {
		return name
	}
	return fmt.Sprintf("%s{%s}", name, strings.Join(parts, ","))
}
