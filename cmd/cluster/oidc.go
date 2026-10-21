package cluster

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"adhar-io/adhar/platform/config"
	pfactory "adhar-io/adhar/platform/providers"
)

var oidcCmd = &cobra.Command{
	Use:   "oidc [name]",
	Short: "Make the cluster's API server trust the platform's Keycloak",
	Long: `Configure the kube-apiserver of a self-managed (compute mode) cluster to
accept Keycloak tokens, so Headlamp and 'adhar auth' sign people in with
their own identity and RBAC.

'adhar up' does this at the end of a cloud bring-up once Keycloak is serving.
Run it for a cluster built before that, or when 'adhar up' reported that
Keycloak was not ready yet. Safe to repeat: an already configured API server
only has its trust file refreshed, which it reloads without a restart.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return enableClusterOIDC(cmd, args[0])
	},
}

func init() {
	oidcCmd.Flags().StringP("provider", "p", "", "Provider hosting the cluster (searched across all configured providers when omitted)")
	oidcCmd.Flags().StringP("file", "f", "", "Path to configuration file")
}

func enableClusterOIDC(cmd *cobra.Command, name string) error {
	providerFlag, _ := cmd.Flags().GetString("provider")
	configFile, _ := cmd.Flags().GetString("file")
	cfg, err := config.LoadConfig(configFile)
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}
	prov, clusterID, err := resolveClusterProvider(cmd, cfg, name, providerFlag)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	kubeconfig, err := prov.GetKubeconfig(ctx, clusterID)
	if err != nil {
		return fmt.Errorf("reading the kubeconfig of %s: %w", name, err)
	}
	line, err := pfactory.EnableClusterOIDC(ctx, prov, name, []byte(kubeconfig))
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "● %s: %s\n", name, line)
	return nil
}
