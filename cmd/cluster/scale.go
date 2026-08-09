/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cluster

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"adhar-io/adhar/platform/config"
	pfactory "adhar-io/adhar/platform/providers"
)

var scaleCmd = &cobra.Command{
	Use:   "scale [name]",
	Short: "Scale a cluster's worker nodes",
	Long: `Scale the worker node count of a cluster.

Scale-up provisions new instances and joins them with kubeadm; scale-down
drains nodes, removes them from the cluster, then deletes the instances.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return scaleCluster(cmd, args[0])
	},
}

func init() {
	scaleCmd.Flags().Int("workers", -1, "Desired worker node count")
	scaleCmd.Flags().String("node-group", "workers", "Node group to scale")
	scaleCmd.Flags().StringP("provider", "p", "", "Provider hosting the cluster (searched across all configured providers when omitted)")
	scaleCmd.Flags().StringP("file", "f", "", "Path to configuration file")
	_ = scaleCmd.MarkFlagRequired("workers")
}

func scaleCluster(cmd *cobra.Command, name string) error {
	workers, _ := cmd.Flags().GetInt("workers")
	if workers < 0 {
		return fmt.Errorf("--workers must be >= 0")
	}
	nodeGroup, _ := cmd.Flags().GetString("node-group")
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

	fmt.Fprintf(cmd.OutOrStdout(), "Scaling cluster %s node group %q to %d workers...\n", name, nodeGroup, workers)
	if err := prov.ScaleNodeGroup(context.Background(), clusterID, nodeGroup, workers); err != nil {
		return fmt.Errorf("failed to scale cluster %s: %w", name, err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "✓ Cluster %s scaled to %d workers\n", name, workers)
	return nil
}

// resolveClusterProvider locates the cluster by name, either in the named
// provider or across all configured providers, returning the provider
// instance and the cluster's provider-native ID.
func resolveClusterProvider(cmd *cobra.Command, cfg *config.Config, name, providerFlag string) (pfactory.Provider, string, error) {
	providers := cfg.Providers
	if providerFlag != "" {
		pc, ok := cfg.Providers[providerFlag]
		if !ok {
			return nil, "", fmt.Errorf("provider %q is not configured", providerFlag)
		}
		providers = map[string]config.ConfigProviderConfig{providerFlag: pc}
	}

	for providerName, providerCfg := range providers {
		p, err := pfactory.DefaultFactory.CreateProvider(providerName, providerCfg.ToProviderMap())
		if err != nil {
			fmt.Fprintf(cmd.OutOrStderr(), "Warning: failed to create provider %s: %v\n", providerName, err)
			continue
		}
		clusters, err := p.ListClusters(context.Background())
		if err != nil {
			fmt.Fprintf(cmd.OutOrStderr(), "Warning: failed to list clusters for provider %s: %v\n", providerName, err)
			continue
		}
		for _, c := range clusters {
			if c.Name == name || c.ID == name {
				return p, c.ID, nil
			}
		}
	}
	return nil, "", fmt.Errorf("cluster %q not found in any configured provider", name)
}
