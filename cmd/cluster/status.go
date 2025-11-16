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
	ptypes "adhar-io/adhar/platform/types"
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
	writeStdout(cmd, "Getting status for cluster: %s\n", name)

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

		printClusterDetails(cmd, cluster)
		printClusterHealth(cmd, p, cluster.ID)
		printClusterMetrics(cmd, p, cluster.ID)
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
				printClusterDetails(cmd, cluster)
				printClusterHealth(cmd, kindProvider, cluster.ID)
				printClusterMetrics(cmd, kindProvider, cluster.ID)
				return nil
			}
		}
	}

	return fmt.Errorf("cluster '%s' not found", name)
}
func printClusterDetails(cmd *cobra.Command, cluster *ptypes.Cluster) {
	writeStdout(cmd, "\nCluster Information:\n")
	writeStdout(cmd, "  Name: %s\n", cluster.Name)
	writeStdout(cmd, "  ID: %s\n", cluster.ID)
	writeStdout(cmd, "  Provider: %s\n", cluster.Provider)
	writeStdout(cmd, "  Region: %s\n", cluster.Region)
	writeStdout(cmd, "  Version: %s\n", cluster.Version)
	writeStdout(cmd, "  Status: %s\n", cluster.Status)
	if cluster.Endpoint != "" {
		writeStdout(cmd, "  Endpoint: %s\n", cluster.Endpoint)
	}
	writeStdout(cmd, "  Created: %s\n", cluster.CreatedAt.Format(time.RFC3339))
	writeStdout(cmd, "  Updated: %s\n", cluster.UpdatedAt.Format(time.RFC3339))
}

func printClusterHealth(cmd *cobra.Command, provider pfactory.Provider, clusterID string) {
	health, err := provider.GetClusterHealth(context.Background(), clusterID)
	if err != nil {
		return
	}

	writeStdout(cmd, "\nHealth Status: %s\n", health.Status)
	for component, componentHealth := range health.Components {
		writeStdout(cmd, "  %s: %s\n", component, componentHealth.Status)
	}
}

func printClusterMetrics(cmd *cobra.Command, provider pfactory.Provider, clusterID string) {
	metrics, err := provider.GetClusterMetrics(context.Background(), clusterID)
	if err != nil {
		return
	}

	writeStdout(cmd, "\nResource Usage:\n")
	writeStdout(cmd, "  CPU: %s / %s (%.1f%%)\n",
		metrics.CPU.Usage, metrics.CPU.Capacity, metrics.CPU.Percent)
	writeStdout(cmd, "  Memory: %s / %s (%.1f%%)\n",
		metrics.Memory.Usage, metrics.Memory.Capacity, metrics.Memory.Percent)
	writeStdout(cmd, "  Disk: %s / %s (%.1f%%)\n",
		metrics.Disk.Usage, metrics.Disk.Capacity, metrics.Disk.Percent)
}
