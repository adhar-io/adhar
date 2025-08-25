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
	"time"

	"github.com/spf13/cobra"

	"adhar-io/adhar/platform/config"
	pfactory "adhar-io/adhar/platform/providers"
)

var statusCmd = &cobra.Command{
	Use:   "status [name]",
	Short: "Get cluster status",
	Long:  "Get detailed status information about a Kubernetes cluster",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return getClusterStatus(cmd, args[0])
	},
}

// getClusterStatus gets detailed cluster status
func getClusterStatus(cmd *cobra.Command, name string) error {
	fmt.Fprintf(cmd.OutOrStdout(), "Getting status for cluster: %s\n", name)

	// Load configuration
	cfg, err := config.LoadConfig("")
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Find the cluster across all providers
	for providerName, providerCfg := range cfg.Providers {
		p, err := pfactory.DefaultFactory.CreateProvider(providerName, providerCfg.ToProviderMap())
		if err != nil {
			continue
		}

		// Try to get cluster with different ID formats
		clusterID := fmt.Sprintf("%s-%s", providerName, name)
		cluster, err := p.GetCluster(context.Background(), clusterID)
		if err != nil {
			continue
		}

		// Found the cluster, display detailed status
		fmt.Fprintf(cmd.OutOrStdout(), "\nCluster Information:\n")
		fmt.Fprintf(cmd.OutOrStdout(), "  Name: %s\n", cluster.Name)
		fmt.Fprintf(cmd.OutOrStdout(), "  ID: %s\n", cluster.ID)
		fmt.Fprintf(cmd.OutOrStdout(), "  Provider: %s\n", cluster.Provider)
		fmt.Fprintf(cmd.OutOrStdout(), "  Region: %s\n", cluster.Region)
		fmt.Fprintf(cmd.OutOrStdout(), "  Version: %s\n", cluster.Version)
		fmt.Fprintf(cmd.OutOrStdout(), "  Status: %s\n", cluster.Status)
		if cluster.Endpoint != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "  Endpoint: %s\n", cluster.Endpoint)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "  Created: %s\n", cluster.CreatedAt.Format(time.RFC3339))
		fmt.Fprintf(cmd.OutOrStdout(), "  Updated: %s\n", cluster.UpdatedAt.Format(time.RFC3339))

		// Get health status
		health, err := p.GetClusterHealth(context.Background(), cluster.ID)
		if err == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "\nHealth Status: %s\n", health.Status)
			for component, componentHealth := range health.Components {
				fmt.Fprintf(cmd.OutOrStdout(), "  %s: %s\n", component, componentHealth.Status)
			}
		}

		// Get metrics
		metrics, err := p.GetClusterMetrics(context.Background(), cluster.ID)
		if err == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "\nResource Usage:\n")
			fmt.Fprintf(cmd.OutOrStdout(), "  CPU: %s / %s (%.1f%%)\n",
				metrics.CPU.Usage, metrics.CPU.Capacity, metrics.CPU.Percent)
			fmt.Fprintf(cmd.OutOrStdout(), "  Memory: %s / %s (%.1f%%)\n",
				metrics.Memory.Usage, metrics.Memory.Capacity, metrics.Memory.Percent)
			fmt.Fprintf(cmd.OutOrStdout(), "  Disk: %s / %s (%.1f%%)\n",
				metrics.Disk.Usage, metrics.Disk.Capacity, metrics.Disk.Percent)
		}

		return nil
	}

	// Also check Kind provider even if not configured (unless already checked)
	kindAlreadyChecked := false
	for providerName := range cfg.Providers {
		if providerName == "kind" {
			kindAlreadyChecked = true
			break
		}
	}

	if !kindAlreadyChecked {
		kindProvider, err := pfactory.DefaultFactory.CreateProvider("kind", map[string]interface{}{
			"kindPath":    "kind",
			"kubectlPath": "kubectl",
		})
		if err == nil {
			clusterID := fmt.Sprintf("kind-%s", name)
			cluster, err := kindProvider.GetCluster(context.Background(), clusterID)
			if err == nil {
				// Found the cluster, display detailed status
				fmt.Fprintf(cmd.OutOrStdout(), "\nCluster Information:\n")
				fmt.Fprintf(cmd.OutOrStdout(), "  Name: %s\n", cluster.Name)
				fmt.Fprintf(cmd.OutOrStdout(), "  ID: %s\n", cluster.ID)
				fmt.Fprintf(cmd.OutOrStdout(), "  Provider: %s\n", cluster.Provider)
				fmt.Fprintf(cmd.OutOrStdout(), "  Region: %s\n", cluster.Region)
				fmt.Fprintf(cmd.OutOrStdout(), "  Version: %s\n", cluster.Version)
				fmt.Fprintf(cmd.OutOrStdout(), "  Status: %s\n", cluster.Status)
				if cluster.Endpoint != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "  Endpoint: %s\n", cluster.Endpoint)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "  Created: %s\n", cluster.CreatedAt.Format(time.RFC3339))
				fmt.Fprintf(cmd.OutOrStdout(), "  Updated: %s\n", cluster.UpdatedAt.Format(time.RFC3339))

				// Get health status
				health, err := kindProvider.GetClusterHealth(context.Background(), cluster.ID)
				if err == nil {
					fmt.Fprintf(cmd.OutOrStdout(), "\nHealth Status: %s\n", health.Status)
					for component, componentHealth := range health.Components {
						fmt.Fprintf(cmd.OutOrStdout(), "  %s: %s\n", component, componentHealth.Status)
					}
				}

				// Get metrics
				metrics, err := kindProvider.GetClusterMetrics(context.Background(), cluster.ID)
				if err == nil {
					fmt.Fprintf(cmd.OutOrStdout(), "\nResource Usage:\n")
					fmt.Fprintf(cmd.OutOrStdout(), "  CPU: %s / %s (%.1f%%)\n",
						metrics.CPU.Usage, metrics.CPU.Capacity, metrics.CPU.Percent)
					fmt.Fprintf(cmd.OutOrStdout(), "  Memory: %s / %s (%.1f%%)\n",
						metrics.Memory.Usage, metrics.Memory.Capacity, metrics.Memory.Percent)
					fmt.Fprintf(cmd.OutOrStdout(), "  Disk: %s / %s (%.1f%%)\n",
						metrics.Disk.Usage, metrics.Disk.Capacity, metrics.Disk.Percent)
				}

				return nil
			}
		}
	}

	return fmt.Errorf("cluster '%s' not found", name)
}
