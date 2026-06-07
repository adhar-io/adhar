package env

import (
	"context"
	"fmt"
	"time"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var deleteForce bool

var deleteCmd = &cobra.Command{
	Use:   "delete [environment-name]",
	Short: "Delete environment",
	Long: `Delete an environment. This removes any CompositeEnvironment XR (best-effort)
and then deletes the environment's namespace and everything in it.

Examples:
  adhar env delete staging
  adhar env delete staging --force   # Skip confirmation`,
	Args: cobra.ExactArgs(1),
	RunE: runDelete,
}

func init() {
	deleteCmd.Flags().BoolVarP(&deleteForce, "force", "f", false, "Delete without confirmation")
}

func runDelete(cmd *cobra.Command, args []string) error {
	envName := args[0]

	if !deleteForce {
		fmt.Printf("🗑️  Delete environment %q and its namespace (all resources)? (y/N): ", envName)
		var resp string
		fmt.Scanln(&resp)
		if resp != "y" && resp != "Y" {
			fmt.Println(helpers.CreateMuted("   Deletion cancelled"))
			return nil
		}
	}

	clientset, err := getClientset()
	if err != nil {
		return unreachable(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Best-effort: remove the CompositeEnvironment XR first.
	if dyn, derr := getDynamicClient(); derr == nil {
		if err := dyn.Resource(compositeEnvironmentGVR).Namespace(envName).Delete(ctx, envName, metav1.DeleteOptions{}); err != nil && !crdMissing(err) && !k8serrors.IsNotFound(err) {
			fmt.Println(helpers.CreateMuted("   CompositeEnvironment XR not deleted: " + err.Error()))
		}
	}

	if err := clientset.CoreV1().Namespaces().Delete(ctx, envName, metav1.DeleteOptions{}); err != nil {
		if k8serrors.IsNotFound(err) {
			return fmt.Errorf("environment %q not found", envName)
		}
		return fmt.Errorf("failed to delete namespace %q: %w", envName, err)
	}

	fmt.Println(helpers.CreateSuccess(fmt.Sprintf("✅ Environment %q deletion initiated (namespace terminating)", envName)))
	return nil
}
