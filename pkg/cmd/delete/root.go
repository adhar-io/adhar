package delete

import (
	"fmt"

	"github.com/adhar-io/adhar/pkg/cmd/helpers"
	"github.com/adhar-io/adhar/pkg/kind"
	"github.com/adhar-io/adhar/pkg/util"
	"github.com/spf13/cobra"
	"sigs.k8s.io/kind/pkg/cluster"
)

var (
	// Flags
	name string
)

var DeleteCmd = &cobra.Command{
	Use:     "down",
	Short:   "Delete an Adhar IDP cluster",
	Long:    ``,
	RunE:    deleteE,
	PreRunE: preDeleteE,
}

func init() {
	// Add the alias here
	DeleteCmd.Aliases = []string{}

	DeleteCmd.PersistentFlags().StringVar(&name, "name", "adhar", "Name of the kind cluster to be deleted.")
}

func preDeleteE(cmd *cobra.Command, args []string) error {
	return helpers.SetLogger()
}

func deleteE(cmd *cobra.Command, args []string) error {
	logger := helpers.CmdLogger
	logger.Info("deleting cluster", "clusterName", name)
	detectOpt, err := util.DetectKindNodeProvider()
	if err != nil {
		return err
	}

	provider := cluster.NewProvider(cluster.ProviderWithLogger(kind.KindLoggerFromLogr(&logger)), detectOpt)
	if err := provider.Delete(name, ""); err != nil {
		return fmt.Errorf("failed to delete cluster %s: %w", name, err)
	}
	return nil
}
