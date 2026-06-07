package storage

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

var optimizeCmd = &cobra.Command{
	Use:   "optimize",
	Short: "Review storage for optimization opportunities",
	Long: `Inspect PersistentVolumes and PersistentVolumeClaims and surface
read-only observations: unbound/pending PVCs and Released or Available PVs that
may be reclaimable. This command only reads cluster state.

Examples:
  adhar storage optimize
  adhar storage optimize --namespace=prod`,
	RunE: runOptimize,
}

func runOptimize(cmd *cobra.Command, args []string) error {
	ns := resolveNamespace()
	logger.Info(fmt.Sprintf("⚡ Reviewing storage in namespace %s...", ns))

	clientset, err := getClientset()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), parseTimeout())
	defer cancel()

	pvs, err := clientset.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing persistent volumes: %w", err)
	}
	pvcs, err := clientset.CoreV1().PersistentVolumeClaims(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing persistent volume claims in %s: %w", ns, err)
	}

	var observations []string
	for _, pv := range pvs.Items {
		switch pv.Status.Phase {
		case corev1.VolumeReleased:
			observations = append(observations, fmt.Sprintf("♻️  PV %s is Released (reclaim policy: %s) — may be reclaimable", pv.Name, pv.Spec.PersistentVolumeReclaimPolicy))
		case corev1.VolumeAvailable:
			observations = append(observations, fmt.Sprintf("💤 PV %s is Available and unbound", pv.Name))
		case corev1.VolumeFailed:
			observations = append(observations, fmt.Sprintf("❌ PV %s is in Failed phase", pv.Name))
		}
	}
	for _, pvc := range pvcs.Items {
		if pvc.Status.Phase != corev1.ClaimBound {
			observations = append(observations, fmt.Sprintf("⚠️  PVC %s is %s (not Bound)", pvc.Name, pvc.Status.Phase))
		}
	}

	if output == "json" {
		return helpers.PrintJSON(map[string]interface{}{"namespace": ns, "observations": observations})
	}
	if output == "yaml" {
		return helpers.PrintYAML(map[string]interface{}{"namespace": ns, "observations": observations})
	}

	fmt.Printf("\n%s\n", helpers.TitleStyle.Render("⚡ Storage Observations"))
	var b strings.Builder
	if len(observations) == 0 {
		b.WriteString("No storage concerns found.\n")
	}
	for _, o := range observations {
		b.WriteString(o + "\n")
	}
	fmt.Println(helpers.BorderStyle.Width(80).Render(b.String()))
	return nil
}
