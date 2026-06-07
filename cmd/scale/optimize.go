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

var optimizeCmd = &cobra.Command{
	Use:   "optimize",
	Short: "Review scaling for optimization opportunities",
	Long: `Inspect Deployments in a namespace and surface read-only scaling
observations: workloads with unready replicas and Deployments that have no
HorizontalPodAutoscaler. This command only reads cluster state; it does not
modify any resources.

Examples:
  adhar scale optimize --namespace=prod
  adhar scale optimize`,
	RunE: runOptimize,
}

func runOptimize(cmd *cobra.Command, args []string) error {
	ns := resolveNamespace()
	logger.Info(fmt.Sprintf("⚡ Reviewing scaling in namespace %s...", ns))

	clientset, err := getClientset()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), parseTimeout())
	defer cancel()

	deployments, err := clientset.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing deployments: %w", err)
	}

	hpas, err := clientset.AutoscalingV2().HorizontalPodAutoscalers(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing horizontalpodautoscalers: %w", err)
	}
	hpaTargets := map[string]bool{}
	for _, h := range hpas.Items {
		if h.Spec.ScaleTargetRef.Kind == "Deployment" {
			hpaTargets[h.Spec.ScaleTargetRef.Name] = true
		}
	}

	var observations []string
	for _, d := range deployments.Items {
		desired := int32(0)
		if d.Spec.Replicas != nil {
			desired = *d.Spec.Replicas
		}
		if desired > 0 && d.Status.ReadyReplicas < desired {
			observations = append(observations, fmt.Sprintf("⚠️  %s has %d/%d ready replicas", d.Name, d.Status.ReadyReplicas, desired))
		}
		if !hpaTargets[d.Name] {
			observations = append(observations, fmt.Sprintf("💡 %s has no HorizontalPodAutoscaler (consider `adhar scale auto --deployment=%s`)", d.Name, d.Name))
		}
	}

	if output == "json" {
		return helpers.PrintJSON(map[string]interface{}{"namespace": ns, "observations": observations})
	}
	if output == "yaml" {
		return helpers.PrintYAML(map[string]interface{}{"namespace": ns, "observations": observations})
	}

	fmt.Printf("\n%s\n", helpers.TitleStyle.Render("⚡ Scaling Observations"))
	var b strings.Builder
	if len(observations) == 0 {
		b.WriteString("No scaling concerns found.\n")
	}
	for _, o := range observations {
		b.WriteString(o + "\n")
	}
	fmt.Println(helpers.BorderStyle.Width(80).Render(b.String()))
	return nil
}
