package gitops

import (
	"context"
	"encoding/json"
	"fmt"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

var (
	rollbackApp       string
	rollbackNamespace string
	rollbackRevision  string
)

var rollbackCmd = &cobra.Command{
	Use:   "rollback [app]",
	Short: "Rollback deployments",
	Long: `Roll an ArgoCD Application back to a prior Git revision.

Sets the Application's spec.source.targetRevision to --revision and triggers a
sync so the control plane redeploys that revision.

Examples:
  adhar gitops rollback --app my-app --revision v1.0.0
  adhar gitops rollback my-app --revision HEAD~1`,
	Args: cobra.MaximumNArgs(1),
	RunE: runRollback,
}

func init() {
	rollbackCmd.Flags().StringVarP(&rollbackApp, "app", "a", "", "Application name (required)")
	rollbackCmd.Flags().StringVarP(&rollbackNamespace, "namespace", "n", argoNamespace, "ArgoCD namespace")
	rollbackCmd.Flags().StringVar(&rollbackRevision, "revision", "", "Git revision/branch/tag to roll back to (required)")
}

func runRollback(cmd *cobra.Command, args []string) error {
	name := rollbackApp
	if name == "" && len(args) > 0 {
		name = args[0]
	}
	if name == "" {
		return fmt.Errorf("--app (or positional app name) is required for rollback")
	}
	if rollbackRevision == "" {
		return fmt.Errorf("--revision is required for rollback")
	}

	logger.Info(fmt.Sprintf("🔄 Rolling back application %s to revision %s", name, rollbackRevision))

	client, err := helpers.DynamicClient()
	if err != nil {
		return err
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	// Point the Application at the target revision and request a sync so the
	// control plane redeploys it.
	patch := map[string]interface{}{
		"metadata": map[string]interface{}{
			"annotations": map[string]interface{}{
				"argocd.argoproj.io/refresh": "hard",
			},
		},
		"spec": map[string]interface{}{
			"source": map[string]interface{}{
				"targetRevision": rollbackRevision,
			},
		},
		"operation": map[string]interface{}{
			"initiatedBy": map[string]interface{}{"username": "adhar-cli"},
			"sync": map[string]interface{}{
				"revision": rollbackRevision,
			},
		},
	}
	body, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("build rollback patch: %w", err)
	}

	if _, err := client.Resource(applicationsGVR).Namespace(rollbackNamespace).
		Patch(ctx, name, types.MergePatchType, body, metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("rollback application %q: %w", name, err)
	}

	fmt.Println(helpers.CreateSuccess(fmt.Sprintf("Application %s rolling back to %s", name, rollbackRevision)))
	return nil
}
