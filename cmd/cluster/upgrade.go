package cluster

import (
	"fmt"

	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
)

var upgradeCmd = &cobra.Command{
	Use:   "upgrade [cluster-name] --version=VERSION",
	Short: "Upgrade cluster version",
	Args:  cobra.ExactArgs(1),
	RunE:  runUpgrade,
}

var upgradeVersion string

func init() {
	upgradeCmd.Flags().StringVarP(&upgradeVersion, "version", "v", "", "Target Kubernetes version")
	cobra.CheckErr(upgradeCmd.MarkFlagRequired("version"))
}

func runUpgrade(cmd *cobra.Command, args []string) error {
	clusterName := args[0]
	logger.Info(fmt.Sprintf("⬆️ Upgrading cluster %s to version %s", clusterName, upgradeVersion))
	// TODO: Implement cluster upgrade
	return nil
}
