package policy

import (
	"context"
	"fmt"
	"time"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	deleteCmd = &cobra.Command{
		Use:   "delete <policy-name>",
		Short: "Delete a Kyverno ClusterPolicy or a compliance-policy XR",
		Long: `Delete a policy by name.

By default a Kyverno ClusterPolicy (kyverno.io/v1) is deleted. With --xr the
named CompositeCompliancePolicy is deleted instead, so the control plane garbage-
collects the ClusterPolicies it composed.

Examples:
  adhar policy delete baseline-require-limits
  adhar policy delete baseline --xr
  adhar policy delete baseline --xr --namespace=team-a`,
		Args: cobra.ExactArgs(1),
		RunE: runDeletePolicy,
	}

	deleteXR bool
)

func init() {
	deleteCmd.Flags().BoolVar(&deleteXR, "xr", false, "Delete the CompositeCompliancePolicy XR instead of a ClusterPolicy")
}

// compositeCompliancePolicyGVR mirrors the XRD plural.
var compositeCompliancePolicyGVR = schema.GroupVersionResource{
	Group: "platform.adhar.io", Version: "v1alpha1", Resource: "compositecompliancepolicies",
}

func runDeletePolicy(cmd *cobra.Command, args []string) error {
	name := args[0]

	dyn, err := getDynamicClient()
	if err != nil {
		return unreachable(err)
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()
	if ctx == nil {
		ctx = context.Background()
	}

	if deleteXR {
		ns := namespace
		if ns == "" {
			ns = "default"
		}
		fmt.Printf("🗑️  Deleting CompositeCompliancePolicy %s (namespace %s)...\n", name, ns)
		if err := dyn.Resource(compositeCompliancePolicyGVR).Namespace(ns).Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
			return fmt.Errorf("delete compliance policy %s: %w", name, err)
		}
		fmt.Println(helpers.CreateSuccess(fmt.Sprintf("CompositeCompliancePolicy %s deleted", name)))
		return nil
	}

	fmt.Printf("🗑️  Deleting Kyverno ClusterPolicy %s...\n", name)
	if err := dyn.Resource(clusterPolicyGVR).Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
		return fmt.Errorf("delete ClusterPolicy %s: %w", name, err)
	}
	fmt.Println(helpers.CreateSuccess(fmt.Sprintf("ClusterPolicy %s deleted", name)))
	return nil
}
