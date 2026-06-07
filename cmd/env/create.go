package env

import (
	"context"
	"fmt"
	"time"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var createTier string

var createCmd = &cobra.Command{
	Use:   "create [environment-name]",
	Short: "Create new environment",
	Long: `Create a new environment. An environment is a namespace labelled
` + "`adhar.io/environment`" + `. If the CompositeEnvironment XRD (platform.adhar.io)
is installed, a CompositeEnvironment XR is also created (best-effort) so that
Crossplane provisions quotas and network policies; otherwise a plain namespace
is created.

Examples:
  adhar env create dev                      # dev-tier environment
  adhar env create staging --tier=staging
  adhar env create prod --tier=prod`,
	Args: cobra.ExactArgs(1),
	RunE: runCreate,
}

func init() {
	createCmd.Flags().StringVar(&createTier, "tier", "dev", "Environment tier: dev, test, staging, prod")
}

func runCreate(cmd *cobra.Command, args []string) error {
	envName := args[0]

	clientset, err := getClientset()
	if err != nil {
		return unreachable(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: envName,
			Labels: map[string]string{
				envLabel:              envName,
				"adhar.io/tier":       createTier,
				"adhar.io/managed-by": "adhar-cli",
			},
		},
	}
	if _, err := clientset.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{}); err != nil {
		if k8serrors.IsAlreadyExists(err) {
			fmt.Println(helpers.WarningStyle.Render(fmt.Sprintf("⚠️  Namespace %q already exists; ensuring labels only", envName)))
		} else {
			return fmt.Errorf("failed to create namespace %q: %w", envName, err)
		}
	} else {
		fmt.Println(helpers.CreateSuccess(fmt.Sprintf("✅ Namespace %q created (tier %q)", envName, createTier)))
	}

	// Best-effort CompositeEnvironment XR for Crossplane-managed quotas/policies.
	if err := tryCreateCompositeEnvironment(ctx, envName, createTier); err != nil {
		if crdMissing(err) {
			fmt.Println(helpers.CreateMuted("   CompositeEnvironment XRD not installed; created a plain namespace."))
		} else {
			fmt.Println(helpers.CreateMuted("   CompositeEnvironment XR not created: " + err.Error()))
		}
	} else {
		fmt.Println(helpers.CreateSuccess(fmt.Sprintf("✅ CompositeEnvironment %q created", envName)))
	}

	return nil
}

// tryCreateCompositeEnvironment creates a namespaced CompositeEnvironment XR.
func tryCreateCompositeEnvironment(ctx context.Context, name, tier string) error {
	dyn, err := getDynamicClient()
	if err != nil {
		return err
	}
	xr := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "platform.adhar.io/v1alpha1",
		"kind":       "CompositeEnvironment",
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": name,
			"labels":    map[string]interface{}{"adhar.io/managed-by": "adhar-cli"},
		},
		"spec": map[string]interface{}{
			"parameters": map[string]interface{}{
				"name": name,
				"tier": tier,
			},
		},
	}}
	_, err = dyn.Resource(compositeEnvironmentGVR).Namespace(name).Create(ctx, xr, metav1.CreateOptions{})
	if k8serrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}
