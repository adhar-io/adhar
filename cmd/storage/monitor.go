package storage

import (
	"context"
	"fmt"
	"strings"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var monitorCmd = &cobra.Command{
	Use:     "monitor",
	Aliases: []string{"status"},
	Short:   "Summarize storage capacity",
	Long: `Summarize storage usage: total provisioned PersistentVolume capacity,
PV phase counts, and PVC phase counts in the namespace.

Examples:
  adhar storage monitor
  adhar storage monitor --namespace=prod`,
	RunE: runMonitor,
}

func runMonitor(cmd *cobra.Command, args []string) error {
	ns := resolveNamespace()
	logger.Info(fmt.Sprintf("📊 Summarizing storage capacity (PVCs in namespace %s)...", ns))

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

	totalPV := resource.NewQuantity(0, resource.BinarySI)
	pvPhases := map[corev1.PersistentVolumePhase]int{}
	for _, pv := range pvs.Items {
		pvPhases[pv.Status.Phase]++
		if q, ok := pv.Spec.Capacity[corev1.ResourceStorage]; ok {
			totalPV.Add(q)
		}
	}

	totalPVC := resource.NewQuantity(0, resource.BinarySI)
	pvcPhases := map[corev1.PersistentVolumeClaimPhase]int{}
	for _, pvc := range pvcs.Items {
		pvcPhases[pvc.Status.Phase]++
		if q, ok := pvc.Status.Capacity[corev1.ResourceStorage]; ok {
			totalPVC.Add(q)
		}
	}

	if output == "json" {
		return helpers.PrintJSON(map[string]interface{}{
			"namespace":        ns,
			"pvCount":          len(pvs.Items),
			"pvTotalCapacity":  totalPV.String(),
			"pvPhases":         stringifyPVPhases(pvPhases),
			"pvcCount":         len(pvcs.Items),
			"pvcBoundCapacity": totalPVC.String(),
			"pvcPhases":        stringifyPVCPhases(pvcPhases),
		})
	}
	if output == "yaml" {
		return helpers.PrintYAML(map[string]interface{}{
			"namespace":        ns,
			"pvCount":          len(pvs.Items),
			"pvTotalCapacity":  totalPV.String(),
			"pvPhases":         stringifyPVPhases(pvPhases),
			"pvcCount":         len(pvcs.Items),
			"pvcBoundCapacity": totalPVC.String(),
			"pvcPhases":        stringifyPVCPhases(pvcPhases),
		})
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("📦 PersistentVolumes:     %d\n", len(pvs.Items)))
	b.WriteString(fmt.Sprintf("💽 Total PV Capacity:     %s\n", totalPV.String()))
	for phase, count := range pvPhases {
		b.WriteString(fmt.Sprintf("   • %-12s %d\n", string(phase), count))
	}
	b.WriteString(fmt.Sprintf("📑 PVCs (%s): %d\n", ns, len(pvcs.Items)))
	b.WriteString(fmt.Sprintf("💾 Bound PVC Capacity:    %s", totalPVC.String()))
	for phase, count := range pvcPhases {
		b.WriteString(fmt.Sprintf("\n   • %-12s %d", string(phase), count))
	}
	fmt.Println(helpers.BorderStyle.Width(60).Render(b.String()))
	return nil
}

func stringifyPVPhases(m map[corev1.PersistentVolumePhase]int) map[string]int {
	out := map[string]int{}
	for k, v := range m {
		out[string(k)] = v
	}
	return out
}

func stringifyPVCPhases(m map[corev1.PersistentVolumeClaimPhase]int) map[string]int {
	out := map[string]int{}
	for k, v := range m {
		out[string(k)] = v
	}
	return out
}
