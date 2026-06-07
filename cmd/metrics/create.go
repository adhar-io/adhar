package metrics

import (
	"context"
	"fmt"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var createCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a recording rule",
	Long: `Create a Prometheus recording rule (PrometheusRule CR).

A recording rule precomputes a PromQL expression and stores the result as a new
metric series. The rule is created as a monitoring.coreos.com/v1 PrometheusRule
that the Prometheus Operator reconciles into the running Prometheus config.

Examples:
  adhar metrics create --name=job_up_ratio --query 'avg(up) by (job)'
  adhar metrics create --name=high_cpu --query 'rate(container_cpu_usage_seconds_total[5m])' --namespace monitoring`,
	RunE: runCreate,
}

func runCreate(cmd *cobra.Command, args []string) error {
	if metricName == "" {
		return fmt.Errorf("--name is required for metric creation")
	}
	if promQueryExpr == "" {
		return fmt.Errorf("--query (the PromQL expression to record) is required")
	}

	ns := namespace
	if ns == "" {
		ns = globals.AdharSystemNamespace
	}

	logger.Info(fmt.Sprintf("📊 Creating recording rule: %s (record %q)", metricName, promQueryExpr))

	dyn, err := getDynamicClient()
	if err != nil {
		return err
	}

	rule := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "monitoring.coreos.com/v1",
			"kind":       "PrometheusRule",
			"metadata": map[string]interface{}{
				"name":      metricName,
				"namespace": ns,
				"labels": map[string]interface{}{
					"app.kubernetes.io/managed-by": "adhar",
					"release":                      "kube-prometheus-stack",
				},
			},
			"spec": map[string]interface{}{
				"groups": []interface{}{
					map[string]interface{}{
						"name": metricName + ".rules",
						"rules": []interface{}{
							map[string]interface{}{
								"record": metricName,
								"expr":   promQueryExpr,
							},
						},
					},
				},
			},
		},
	}

	created, err := dyn.Resource(prometheusRulesGVR).Namespace(ns).Create(context.Background(), rule, metav1.CreateOptions{})
	if err != nil {
		return friendlyCRDError("PrometheusRule", err)
	}

	fmt.Println(helpers.CreateSuccess(fmt.Sprintf("✅ Created PrometheusRule %s/%s", created.GetNamespace(), created.GetName())))
	return nil
}
