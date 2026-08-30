package policy

import (
	"context"
	"fmt"
	"os"
	"time"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
)

var (
	applyCmd = &cobra.Command{
		Use:   "apply [policy-file]",
		Short: "Apply a policy through the control plane (or a raw Kyverno file)",
		Long: `Apply a compliance policy.

Default (control-plane) mode creates a CompositeCompliancePolicy XR. The
Crossplane control plane reconciles it into Kyverno ClusterPolicies — the same
declarative path the Adhar Console uses, so a policy request is identical
regardless of entry point.

Power-user mode (--file) applies a raw Kyverno policy manifest (ClusterPolicy /
Policy, or any Kubernetes manifest) directly via server-side apply.

Examples:
  adhar policy apply --name=baseline --mode=enforce
  adhar policy apply --name=baseline --namespace=team-a
  adhar policy apply --file=my-clusterpolicy.yaml
  adhar policy apply --file=my-clusterpolicy.yaml --dry-run`,
		Args: cobra.MaximumNArgs(1),
		RunE: runApplyPolicy,
	}

	// Apply-specific flags.
	applyName string
	applyMode string
)

func init() {
	applyCmd.Flags().StringVar(&applyName, "name", "", "Name for the compliance policy XR (control-plane mode)")
	applyCmd.Flags().StringVar(&applyMode, "mode", "audit", "Enforcement mode: audit or enforce (control-plane mode)")
}

func runApplyPolicy(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		policyFile = args[0]
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
	defer cancel()
	if ctx == nil {
		ctx = context.Background()
	}

	// Power-user path: apply a raw manifest directly.
	if policyFile != "" {
		return applyPolicyFile(ctx, policyFile)
	}

	// Control-plane path: create a CompositeCompliancePolicy XR.
	return applyPolicyXR(ctx)
}

// applyPolicyFile server-side-applies each document in a Kyverno policy file.
func applyPolicyFile(ctx context.Context, file string) error {
	fmt.Println(helpers.TitleStyle.Render("📋 Applying policy manifest: " + file))

	data, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("read policy file: %w", err)
	}
	objs, err := decodeManifests(data)
	if err != nil {
		return err
	}
	if len(objs) == 0 {
		return fmt.Errorf("no manifests found in %s", file)
	}

	dyn, mapper, err := kubeClients()
	if err != nil {
		return unreachable(err)
	}

	for _, obj := range objs {
		if err := applyManifest(ctx, dyn, mapper, obj, namespace, dryRun); err != nil {
			return fmt.Errorf("apply %s/%s: %w", obj.GetKind(), obj.GetName(), err)
		}
		verb := "applied"
		if dryRun {
			verb = "validated (dry-run)"
		}
		fmt.Printf("   ✅ %s %s/%s\n", verb, obj.GetKind(), obj.GetName())
	}
	fmt.Println(helpers.CreateSuccess("Policy manifest applied successfully."))
	return nil
}

// applyPolicyXR creates a CompositeCompliancePolicy composite resource.
func applyPolicyXR(ctx context.Context) error {
	if applyName == "" {
		return fmt.Errorf("--name is required (control-plane mode), or use --file to apply a raw policy manifest")
	}
	if applyMode != "audit" && applyMode != "enforce" {
		return fmt.Errorf("--mode must be 'audit' or 'enforce' (got %q)", applyMode)
	}

	ns := namespace
	if ns == "" {
		ns = "default"
	}

	fmt.Println(helpers.TitleStyle.Render(fmt.Sprintf("📋 Creating compliance policy %q (mode: %s, provider: %s)",
		applyName, applyMode, helpers.ActiveProvider())))

	spec := map[string]interface{}{
		// Satisfy the XRD's required top-level compositionSelector as well as the
		// v2 spec.crossplane selector that NewXR adds.
		"compositionSelector": helpers.CompositionSelector("compliance", nil),
		"parameters": map[string]interface{}{
			"displayName":     applyName,
			"policyEngine":    "kyverno",
			"enforcementMode": applyMode,
		},
	}

	xr := helpers.NewXR("CompositeCompliancePolicy", applyName, ns, "compliance", nil, spec)

	if dryRun {
		fmt.Println(helpers.CreateMuted("   Dry run — would create CompositeCompliancePolicy:"))
		return helpers.PrintYAML(xr.Object)
	}

	if err := helpers.ApplyXR(ctx, "compositecompliancepolicies", xr); err != nil {
		return fmt.Errorf("create compliance policy: %w", err)
	}

	fmt.Println(helpers.CreateSuccess(fmt.Sprintf("CompositeCompliancePolicy %s created in namespace %s", applyName, ns)))
	fmt.Println(helpers.CreateMuted("   The control plane will reconcile it into Kyverno ClusterPolicies."))
	return nil
}
