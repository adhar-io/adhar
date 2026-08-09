package cluster

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"adhar-io/adhar/platform/config"
)

var upgradeCmd = &cobra.Command{
	Use:   "upgrade [cluster-name] --version=VERSION",
	Short: "Upgrade cluster Kubernetes version",
	Long: `Upgrade a cluster's Kubernetes version in place.

Self-managed (compute) clusters upgrade via kubeadm over SSH — control plane
first, then each worker. Managed clusters upgrade through the cloud API.`,
	Args: cobra.ExactArgs(1),
	RunE: runUpgrade,
}

var upgradeVersion string

func init() {
	upgradeCmd.Flags().StringVarP(&upgradeVersion, "version", "v", "", "Target Kubernetes version (e.g. 1.34.2)")
	upgradeCmd.Flags().StringP("provider", "p", "", "Provider hosting the cluster (searched across all configured providers when omitted)")
	upgradeCmd.Flags().StringP("file", "f", "", "Path to configuration file")
	_ = upgradeCmd.MarkFlagRequired("version")
}

func runUpgrade(cmd *cobra.Command, args []string) error {
	clusterName := args[0]
	providerFlag, _ := cmd.Flags().GetString("provider")
	configFile, _ := cmd.Flags().GetString("file")

	cfg, err := config.LoadConfig(configFile)
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	prov, clusterID, err := resolveClusterProvider(cmd, cfg, clusterName, providerFlag)
	if err != nil {
		return err
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Upgrading cluster %s to Kubernetes %s (control plane first, then workers)...\n", clusterName, upgradeVersion)
	if err := prov.UpgradeCluster(context.Background(), clusterID, upgradeVersion); err != nil {
		return fmt.Errorf("failed to upgrade cluster %s: %w", clusterName, err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "✓ Cluster %s upgraded to %s\n", clusterName, upgradeVersion)
	return nil
}
