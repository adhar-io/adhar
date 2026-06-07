package scale

import (
	"context"
	"fmt"

	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
)

var (
	hpaMin int
	hpaMax int
	hpaCPU int
)

var autoCmd = &cobra.Command{
	Use:   "auto",
	Short: "Configure auto-scaling",
	Long: `Configure horizontal pod auto-scaling (HPA).

Creates or updates an autoscaling/v2 HorizontalPodAutoscaler targeting the
named Deployment or StatefulSet, scaling on average CPU utilization.

Examples:
  adhar scale auto --deployment=worker --min=2 --max=10 --cpu=70
  adhar scale auto --deployment=api`,
	RunE: runAuto,
}

func init() {
	autoCmd.Flags().IntVar(&hpaMin, "min", 1, "Minimum replicas")
	autoCmd.Flags().IntVar(&hpaMax, "max", 5, "Maximum replicas")
	autoCmd.Flags().IntVar(&hpaCPU, "cpu", 80, "Target average CPU utilization percentage")
}

func runAuto(cmd *cobra.Command, args []string) error {
	if deploymentName == "" {
		return fmt.Errorf("--deployment is required for auto-scaling configuration")
	}
	if hpaMin <= 0 {
		return fmt.Errorf("--min must be greater than 0")
	}
	if hpaMax < hpaMin {
		return fmt.Errorf("--max (%d) must be >= --min (%d)", hpaMax, hpaMin)
	}
	if hpaCPU <= 0 || hpaCPU > 100 {
		return fmt.Errorf("--cpu must be between 1 and 100")
	}

	ns := resolveNamespace()
	logger.Info(fmt.Sprintf("🤖 Configuring auto-scaling for %s/%s (min=%d max=%d cpu=%d%%)", ns, deploymentName, hpaMin, hpaMax, hpaCPU))

	clientset, err := getClientset()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), parseTimeout())
	defer cancel()

	kind, err := detectWorkload(ctx, clientset, ns, deploymentName)
	if err != nil {
		return err
	}

	action, err := ensureHPA(ctx, clientset, ns, deploymentName, kind, int32(hpaMin), int32(hpaMax), int32(hpaCPU))
	if err != nil {
		return err
	}

	logger.Info(fmt.Sprintf("✅ HPA %s for %s %s/%s", action, kind, ns, deploymentName))
	return nil
}
