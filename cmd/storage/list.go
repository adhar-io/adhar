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

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List storage resources",
	Long: `List PersistentVolumes, PersistentVolumeClaims, and StorageClasses.

PVCs are listed for the chosen namespace; PVs and StorageClasses are
cluster-scoped.

Examples:
  adhar storage list
  adhar storage list --namespace=prod
  adhar storage list --output=json`,
	RunE: runList,
}

func runList(cmd *cobra.Command, args []string) error {
	ns := resolveNamespace()
	logger.Info(fmt.Sprintf("📋 Listing storage resources (PVCs in namespace %s)...", ns))

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
	scs, err := clientset.StorageV1().StorageClasses().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing storage classes: %w", err)
	}

	if output == "json" {
		return helpers.PrintJSON(map[string]interface{}{
			"persistentVolumes":      pvs.Items,
			"persistentVolumeClaims": pvcs.Items,
			"storageClasses":         scs.Items,
		})
	}
	if output == "yaml" {
		return helpers.PrintYAML(map[string]interface{}{
			"persistentVolumes":      pvs.Items,
			"persistentVolumeClaims": pvcs.Items,
			"storageClasses":         scs.Items,
		})
	}

	fmt.Printf("\n%s\n", helpers.TitleStyle.Render("💾 StorageClasses"))
	var sct strings.Builder
	sct.WriteString(fmt.Sprintf("%-28s %-30s %-10s\n", "NAME", "PROVISIONER", "DEFAULT"))
	sct.WriteString(strings.Repeat("─", 70) + "\n")
	if len(scs.Items) == 0 {
		sct.WriteString("(none)\n")
	}
	for _, sc := range scs.Items {
		def := "false"
		if sc.Annotations["storageclass.kubernetes.io/is-default-class"] == "true" {
			def = "true"
		}
		sct.WriteString(fmt.Sprintf("%-28s %-30s %-10s\n", sc.Name, sc.Provisioner, def))
	}
	fmt.Println(helpers.BorderStyle.Width(75).Render(sct.String()))

	fmt.Printf("\n%s\n", helpers.TitleStyle.Render("📦 PersistentVolumes"))
	var pvt strings.Builder
	pvt.WriteString(fmt.Sprintf("%-28s %-10s %-12s %-12s %-20s\n", "NAME", "CAPACITY", "STATUS", "CLASS", "CLAIM"))
	pvt.WriteString(strings.Repeat("─", 85) + "\n")
	if len(pvs.Items) == 0 {
		pvt.WriteString("(none)\n")
	}
	for _, pv := range pvs.Items {
		claim := ""
		if pv.Spec.ClaimRef != nil {
			claim = pv.Spec.ClaimRef.Namespace + "/" + pv.Spec.ClaimRef.Name
		}
		pvt.WriteString(fmt.Sprintf("%-28s %-10s %-12s %-12s %-20s\n",
			pv.Name, pvCapacity(pv.Spec.Capacity), string(pv.Status.Phase), pv.Spec.StorageClassName, claim))
	}
	fmt.Println(helpers.BorderStyle.Width(90).Render(pvt.String()))

	fmt.Printf("\n%s\n", helpers.TitleStyle.Render("📑 PersistentVolumeClaims ("+ns+")"))
	var pvct strings.Builder
	pvct.WriteString(fmt.Sprintf("%-28s %-12s %-10s %-12s\n", "NAME", "STATUS", "CAPACITY", "CLASS"))
	pvct.WriteString(strings.Repeat("─", 70) + "\n")
	if len(pvcs.Items) == 0 {
		pvct.WriteString("(none)\n")
	}
	for _, pvc := range pvcs.Items {
		class := ""
		if pvc.Spec.StorageClassName != nil {
			class = *pvc.Spec.StorageClassName
		}
		pvct.WriteString(fmt.Sprintf("%-28s %-12s %-10s %-12s\n",
			pvc.Name, string(pvc.Status.Phase), pvCapacity(pvc.Status.Capacity), class))
	}
	fmt.Println(helpers.BorderStyle.Width(75).Render(pvct.String()))

	return nil
}

func pvCapacity(res corev1.ResourceList) string {
	if q, ok := res[corev1.ResourceStorage]; ok {
		return q.String()
	}
	return "<unset>"
}
