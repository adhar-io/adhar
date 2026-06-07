package scale

import (
	"context"
	"fmt"

	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
)

var upCmd = &cobra.Command{
	Use:   "up",
	Short: "Scale up resources",
	Long: `Scale up deployments and resources.
	
Examples:
  adhar scale up --deployment=web --replicas=5
  adhar scale up --deployment=api --replicas=3`,
	RunE: runUp,
}

func runUp(cmd *cobra.Command, args []string) error {
	if deploymentName == "" {
		return fmt.Errorf("--deployment is required for scaling up")
	}

	if replicas <= 0 {
		return fmt.Errorf("--replicas must be greater than 0")
	}

	ns := resolveNamespace()
	logger.Info(fmt.Sprintf("⬆️ Scaling up %s/%s to %d replicas", ns, deploymentName, replicas))

	clientset, err := getClientset()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), parseTimeout())
	defer cancel()

	kind, err := applyReplicas(ctx, clientset, ns, deploymentName, int32(replicas))
	if err != nil {
		return err
	}

	logger.Info(fmt.Sprintf("✅ %s %s/%s scaled to %d replicas", kind, ns, deploymentName, replicas))
	return nil
}
