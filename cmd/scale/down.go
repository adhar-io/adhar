package scale

import (
	"context"
	"fmt"

	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
)

var downCmd = &cobra.Command{
	Use:   "down",
	Short: "Scale down resources",
	Long: `Scale down deployments and resources.
	
Examples:
  adhar scale down --deployment=web --replicas=2
  adhar scale down --deployment=api --replicas=1`,
	RunE: runDown,
}

func runDown(cmd *cobra.Command, args []string) error {
	if deploymentName == "" {
		return fmt.Errorf("--deployment is required for scaling down")
	}

	if replicas <= 0 {
		return fmt.Errorf("--replicas must be greater than 0")
	}

	ns := resolveNamespace()
	logger.Info(fmt.Sprintf("⬇️ Scaling down %s/%s to %d replicas", ns, deploymentName, replicas))

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
