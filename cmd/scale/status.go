package scale

import (
	"context"
	"fmt"
	"strings"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check scaling status",
	Long: `Check scaling status and current vs desired replica counts.

Lists Deployments and StatefulSets (with current/desired/ready replicas) and any
HorizontalPodAutoscalers in the namespace. Filter to a single workload with
--deployment.

Examples:
  adhar scale status --deployment=web
  adhar scale status --namespace=prod
  adhar scale status`,
	RunE: runStatus,
}

type scaleRow struct {
	Kind    string
	Name    string
	Desired int32
	Current int32
	Ready   int32
}

func runStatus(cmd *cobra.Command, args []string) error {
	ns := resolveNamespace()
	logger.Info(fmt.Sprintf("📊 Checking scaling status in namespace %s...", ns))

	clientset, err := getClientset()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), parseTimeout())
	defer cancel()

	var rows []scaleRow

	deployments, err := clientset.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing deployments: %w", err)
	}
	for _, d := range deployments.Items {
		if deploymentName != "" && d.Name != deploymentName {
			continue
		}
		desired := int32(0)
		if d.Spec.Replicas != nil {
			desired = *d.Spec.Replicas
		}
		rows = append(rows, scaleRow{"Deployment", d.Name, desired, d.Status.Replicas, d.Status.ReadyReplicas})
	}

	statefulSets, err := clientset.AppsV1().StatefulSets(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing statefulsets: %w", err)
	}
	for _, s := range statefulSets.Items {
		if deploymentName != "" && s.Name != deploymentName {
			continue
		}
		desired := int32(0)
		if s.Spec.Replicas != nil {
			desired = *s.Spec.Replicas
		}
		rows = append(rows, scaleRow{"StatefulSet", s.Name, desired, s.Status.Replicas, s.Status.ReadyReplicas})
	}

	hpas, err := clientset.AutoscalingV2().HorizontalPodAutoscalers(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing horizontalpodautoscalers: %w", err)
	}

	if output == "json" {
		return helpers.PrintJSON(map[string]interface{}{"workloads": rows, "hpaCount": len(hpas.Items)})
	}
	if output == "yaml" {
		return helpers.PrintYAML(map[string]interface{}{"workloads": rows, "hpaCount": len(hpas.Items)})
	}

	fmt.Printf("\n%s\n", helpers.TitleStyle.Render("🔄 Workload Replicas"))
	var t strings.Builder
	t.WriteString(fmt.Sprintf("%-13s %-32s %-9s %-9s %-9s\n", "KIND", "NAME", "DESIRED", "CURRENT", "READY"))
	t.WriteString(strings.Repeat("─", 75) + "\n")
	if len(rows) == 0 {
		t.WriteString("(none)\n")
	}
	for _, r := range rows {
		t.WriteString(fmt.Sprintf("%-13s %-32s %-9d %-9d %-9d\n", r.Kind, r.Name, r.Desired, r.Current, r.Ready))
	}
	fmt.Println(helpers.BorderStyle.Width(80).Render(t.String()))

	fmt.Printf("\n%s\n", helpers.TitleStyle.Render("🤖 HorizontalPodAutoscalers"))
	var h strings.Builder
	h.WriteString(fmt.Sprintf("%-28s %-18s %-9s %-9s %-9s\n", "NAME", "TARGET", "MIN", "MAX", "CURRENT"))
	h.WriteString(strings.Repeat("─", 75) + "\n")
	if len(hpas.Items) == 0 {
		h.WriteString("(none)\n")
	}
	for _, hpa := range hpas.Items {
		if deploymentName != "" && hpa.Spec.ScaleTargetRef.Name != deploymentName {
			continue
		}
		minR := int32(0)
		if hpa.Spec.MinReplicas != nil {
			minR = *hpa.Spec.MinReplicas
		}
		target := fmt.Sprintf("%s/%s", hpa.Spec.ScaleTargetRef.Kind, hpa.Spec.ScaleTargetRef.Name)
		h.WriteString(fmt.Sprintf("%-28s %-18s %-9d %-9d %-9d\n",
			hpa.Name, target, minR, hpa.Spec.MaxReplicas, hpa.Status.CurrentReplicas))
	}
	fmt.Println(helpers.BorderStyle.Width(80).Render(h.String()))

	return nil
}
