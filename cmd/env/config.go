package env

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

var configCmd = &cobra.Command{
	Use:   "config [environment-name]",
	Short: "Show or set environment configuration",
	Long: `Show or update a CompositeEnvironment XR's parameters (resource quotas and
limits) through the control plane. With no setter flags the current parameters
and status are shown; passing any setter patches the XR and Crossplane reconciles
the change (ResourceQuota / LimitRange / NetworkPolicy).

Examples:
  adhar env config dev                          # Show config
  adhar env config dev --cpu=8 --memory=16Gi    # Update quotas
  adhar env config dev --pods=40 --tier=staging
  adhar env config dev --network-policy=false`,
	Args: cobra.ExactArgs(1),
	RunE: runConfig,
}

var (
	cfgCPU       string
	cfgMemory    string
	cfgPods      int
	cfgTier      string
	cfgNetPolSet bool
	cfgNetPol    bool
)

func init() {
	configCmd.Flags().StringVar(&cfgCPU, "cpu", "", "CPU quota (e.g. 4, 8)")
	configCmd.Flags().StringVar(&cfgMemory, "memory", "", "Memory quota (e.g. 8Gi, 16Gi)")
	configCmd.Flags().IntVar(&cfgPods, "pods", -1, "Pod quota")
	configCmd.Flags().StringVar(&cfgTier, "tier", "", "Environment tier (dev, test, staging, prod)")
	configCmd.Flags().BoolVar(&cfgNetPol, "network-policy", true, "Enable the default-deny NetworkPolicy")
}

func runConfig(cmd *cobra.Command, args []string) error {
	envName := args[0]
	cfgNetPolSet = cmd.Flags().Changed("network-policy")

	dyn, err := getDynamicClient()
	if err != nil {
		return unreachable(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Any setter flag => patch; otherwise show.
	params := map[string]interface{}{}
	if cfgCPU != "" {
		params["cpuQuota"] = cfgCPU
	}
	if cfgMemory != "" {
		params["memoryQuota"] = cfgMemory
	}
	if cfgPods >= 0 {
		params["podQuota"] = cfgPods
	}
	if cfgTier != "" {
		params["tier"] = cfgTier
	}
	if cfgNetPolSet {
		params["enableNetworkPolicy"] = cfgNetPol
	}

	if len(params) > 0 {
		patch := map[string]interface{}{"spec": map[string]interface{}{"parameters": params}}
		data, err := json.Marshal(patch)
		if err != nil {
			return fmt.Errorf("build patch: %w", err)
		}
		logger.Info(fmt.Sprintf("⚙️  Updating configuration for environment: %s", envName))
		if _, err := dyn.Resource(compositeEnvironmentGVR).Namespace(envName).
			Patch(ctx, envName, types.MergePatchType, data, metav1.PatchOptions{}); err != nil {
			if crdMissing(err) {
				return fmt.Errorf("CompositeEnvironment XRD not installed; cannot manage config for %q", envName)
			}
			if k8serrors.IsNotFound(err) {
				return fmt.Errorf("environment %q has no CompositeEnvironment XR", envName)
			}
			return fmt.Errorf("patch environment config: %w", err)
		}
		fmt.Println(helpers.CreateSuccess(fmt.Sprintf("✅ Environment %q configuration updated", envName)))
		return nil
	}

	return showEnvironmentConfig(ctx, envName)
}

func showEnvironmentConfig(ctx context.Context, envName string) error {
	logger.Info(fmt.Sprintf("📋 Configuration for environment: %s", envName))

	dyn, err := getDynamicClient()
	if err != nil {
		return unreachable(err)
	}

	xr, err := dyn.Resource(compositeEnvironmentGVR).Namespace(envName).Get(ctx, envName, metav1.GetOptions{})
	if err != nil {
		if crdMissing(err) || k8serrors.IsNotFound(err) {
			return fmt.Errorf("no CompositeEnvironment XR for %q (create it with `adhar env create %s`)", envName, envName)
		}
		return fmt.Errorf("get environment config: %w", err)
	}

	o := xr.Object
	var b string
	add := func(label, val string) { b += fmt.Sprintf("%s %s\n", helpers.BulletStyle.Render(label), val) }
	add("Environment:", envName)
	add("Tier:", envString(o, "spec", "parameters", "tier"))
	add("CPU Quota:", envString(o, "spec", "parameters", "cpuQuota"))
	add("Memory Quota:", envString(o, "spec", "parameters", "memoryQuota"))
	add("Pod Quota:", envString(o, "spec", "parameters", "podQuota"))
	add("Network Policy:", envString(o, "spec", "parameters", "enableNetworkPolicy"))
	add("Phase:", envString(o, "status", "phase"))
	add("Namespace:", envString(o, "status", "namespace"))
	fmt.Println(helpers.CreateBox(b, 80))
	return nil
}

// envString reads a nested value from an unstructured map as a display string,
// tolerating string/bool/number types.
func envString(obj map[string]interface{}, keys ...string) string {
	cur := obj
	for i, k := range keys {
		if i == len(keys)-1 {
			v, ok := cur[k]
			if !ok || v == nil {
				return "-"
			}
			return fmt.Sprintf("%v", v)
		}
		next, ok := cur[k].(map[string]interface{})
		if !ok {
			return "-"
		}
		cur = next
	}
	return "-"
}
