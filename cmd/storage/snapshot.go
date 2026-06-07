package storage

import (
	"context"
	"fmt"
	"strings"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/k8s"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var snapshotCmd = &cobra.Command{
	Use:   "snapshot",
	Short: "List volume snapshots",
	Long: `List VolumeSnapshots (snapshot.storage.k8s.io/v1) in a namespace.

Requires the external-snapshotter CRDs to be installed; if they are not present
the command reports that snapshots are unsupported on this cluster.

Examples:
  adhar storage snapshot
  adhar storage snapshot --namespace=prod`,
	RunE: runSnapshot,
}

var volumeSnapshotGVR = schema.GroupVersionResource{
	Group:    "snapshot.storage.k8s.io",
	Version:  "v1",
	Resource: "volumesnapshots",
}

func runSnapshot(cmd *cobra.Command, args []string) error {
	ns := resolveNamespace()
	logger.Info(fmt.Sprintf("📸 Listing volume snapshots in namespace %s...", ns))

	// Snapshots are CRD-backed; use the dynamic client.
	dyn, err := k8s.GetDynamicClient()
	if err != nil {
		fmt.Println(helpers.ErrorStyle.Render("❌ Could not connect to the cluster"))
		fmt.Println(helpers.CreateMuted("   " + err.Error()))
		return fmt.Errorf("failed to get dynamic client: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), parseTimeout())
	defer cancel()

	list, err := dyn.Resource(volumeSnapshotGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) || strings.Contains(err.Error(), "could not find the requested resource") {
			fmt.Println(helpers.CreateMuted("ℹ️  VolumeSnapshot CRDs are not installed on this cluster."))
			return nil
		}
		return fmt.Errorf("listing volume snapshots in %s: %w", ns, err)
	}

	if output == "json" {
		return helpers.PrintJSON(list.Items)
	}
	if output == "yaml" {
		return helpers.PrintYAML(list.Items)
	}

	fmt.Printf("\n%s\n", helpers.TitleStyle.Render("📸 Volume Snapshots"))
	var t strings.Builder
	t.WriteString(fmt.Sprintf("%-32s %-28s %-12s\n", "NAME", "SOURCE PVC", "READY"))
	t.WriteString(strings.Repeat("─", 75) + "\n")
	if len(list.Items) == 0 {
		t.WriteString("(none)\n")
	}
	for _, item := range list.Items {
		name := item.GetName()
		srcPVC, _, _ := unstructuredString(item.Object, "spec", "source", "persistentVolumeClaimName")
		ready, _, _ := unstructuredString(item.Object, "status", "readyToUse")
		t.WriteString(fmt.Sprintf("%-32s %-28s %-12s\n", name, srcPVC, ready))
	}
	fmt.Println(helpers.BorderStyle.Width(80).Render(t.String()))
	return nil
}

// unstructuredString fetches a nested string-ish field, formatting non-string
// scalars (e.g. bool) with %v.
func unstructuredString(obj map[string]interface{}, fields ...string) (string, bool, error) {
	cur := interface{}(obj)
	for _, f := range fields {
		m, ok := cur.(map[string]interface{})
		if !ok {
			return "", false, nil
		}
		cur, ok = m[f]
		if !ok {
			return "", false, nil
		}
	}
	if cur == nil {
		return "", false, nil
	}
	return fmt.Sprintf("%v", cur), true, nil
}
