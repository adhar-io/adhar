package network

import (
	"context"
	"fmt"
	"os"
	"strings"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
	networkingv1 "k8s.io/api/networking/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

var policyCmd = &cobra.Command{
	Use:   "policy",
	Short: "Manage network policies",
	Long: `List, apply, validate, or delete NetworkPolicies (networking.k8s.io/v1).

Examples:
  adhar network policy                              # list policies (default)
  adhar network policy --action=apply --file=policy.yaml
  adhar network policy --action=validate --file=policy.yaml
  adhar network policy --action=delete --policy=deny-all`,
	RunE: runPolicy,
}

var (
	policyFile   string
	policyAction string
)

func init() {
	policyCmd.Flags().StringVarP(&policyFile, "file", "f", "", "Policy file path")
	policyCmd.Flags().StringVarP(&policyAction, "action", "a", "", "Action (apply, validate, delete); default lists policies")
}

func runPolicy(cmd *cobra.Command, args []string) error {
	switch policyAction {
	case "apply":
		return applyPolicy(policyFile)
	case "validate":
		return validatePolicy(policyFile)
	case "delete":
		return deletePolicy(policy)
	default:
		return listPolicies()
	}
}

// loadPolicy reads and decodes a NetworkPolicy manifest from disk.
func loadPolicy(filePath string) (*networkingv1.NetworkPolicy, error) {
	if filePath == "" {
		return nil, fmt.Errorf("--file is required")
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", filePath, err)
	}
	np := &networkingv1.NetworkPolicy{}
	if err := yaml.Unmarshal(data, np); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", filePath, err)
	}
	if np.Kind != "" && np.Kind != "NetworkPolicy" {
		return nil, fmt.Errorf("expected kind NetworkPolicy, got %q", np.Kind)
	}
	if np.Name == "" {
		return nil, fmt.Errorf("policy in %s has no metadata.name", filePath)
	}
	return np, nil
}

func applyPolicy(filePath string) error {
	np, err := loadPolicy(filePath)
	if err != nil {
		return err
	}
	ns := np.Namespace
	if ns == "" {
		ns = resolveNamespace()
		np.Namespace = ns
	}
	logger.Info(fmt.Sprintf("🛡️ Applying network policy %s/%s from %s", ns, np.Name, filePath))

	clientset, err := getClientset()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), parseTimeout())
	defer cancel()

	client := clientset.NetworkingV1().NetworkPolicies(ns)
	existing, getErr := client.Get(ctx, np.Name, metav1.GetOptions{})
	if getErr != nil {
		if k8serrors.IsNotFound(getErr) {
			if _, err := client.Create(ctx, np, metav1.CreateOptions{}); err != nil {
				return fmt.Errorf("creating network policy %s/%s: %w", ns, np.Name, err)
			}
			logger.Info(fmt.Sprintf("✅ Network policy %s/%s created", ns, np.Name))
			return nil
		}
		return fmt.Errorf("getting network policy %s/%s: %w", ns, np.Name, getErr)
	}

	existing.Spec = np.Spec
	if _, err := client.Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("updating network policy %s/%s: %w", ns, np.Name, err)
	}
	logger.Info(fmt.Sprintf("✅ Network policy %s/%s updated", ns, np.Name))
	return nil
}

func validatePolicy(filePath string) error {
	np, err := loadPolicy(filePath)
	if err != nil {
		return err
	}
	logger.Info(fmt.Sprintf("✅ Valid NetworkPolicy %q (%d ingress, %d egress rule(s), %d policy type(s))",
		np.Name, len(np.Spec.Ingress), len(np.Spec.Egress), len(np.Spec.PolicyTypes)))
	return nil
}

func deletePolicy(policyName string) error {
	if policyName == "" {
		return fmt.Errorf("--policy is required for deletion")
	}
	ns := resolveNamespace()
	logger.Info(fmt.Sprintf("🗑️ Deleting network policy %s/%s", ns, policyName))

	clientset, err := getClientset()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), parseTimeout())
	defer cancel()

	if err := clientset.NetworkingV1().NetworkPolicies(ns).Delete(ctx, policyName, metav1.DeleteOptions{}); err != nil {
		if k8serrors.IsNotFound(err) {
			return fmt.Errorf("network policy %s/%s not found", ns, policyName)
		}
		return fmt.Errorf("deleting network policy %s/%s: %w", ns, policyName, err)
	}
	logger.Info(fmt.Sprintf("✅ Network policy %s/%s deleted", ns, policyName))
	return nil
}

func listPolicies() error {
	ns := resolveNamespace()
	logger.Info(fmt.Sprintf("📋 Listing network policies in namespace %s...", ns))

	clientset, err := getClientset()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), parseTimeout())
	defer cancel()

	policies, err := clientset.NetworkingV1().NetworkPolicies(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing network policies in %s: %w", ns, err)
	}

	if output == "json" {
		return helpers.PrintJSON(policies.Items)
	}
	if output == "yaml" {
		return helpers.PrintYAML(policies.Items)
	}

	fmt.Printf("\n%s\n", helpers.TitleStyle.Render("🛡️  Network Policies"))
	var t strings.Builder
	t.WriteString(fmt.Sprintf("%-32s %-28s %-18s\n", "NAME", "POD SELECTOR", "POLICY TYPES"))
	t.WriteString(strings.Repeat("─", 80) + "\n")
	if len(policies.Items) == 0 {
		t.WriteString("(none)\n")
	}
	for _, p := range policies.Items {
		var types []string
		for _, pt := range p.Spec.PolicyTypes {
			types = append(types, string(pt))
		}
		t.WriteString(fmt.Sprintf("%-32s %-28s %-18s\n",
			p.Name, selectorString(p.Spec.PodSelector.MatchLabels), strings.Join(types, ",")))
	}
	fmt.Println(helpers.BorderStyle.Width(85).Render(t.String()))
	return nil
}
