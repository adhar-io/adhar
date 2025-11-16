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
	ptypes "adhar-io/adhar/platform/types"
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List all clusters",
	Long:  "List all Kubernetes clusters across all configured providers",
	RunE: func(cmd *cobra.Command, args []string) error {
		return listClusters(cmd)
	},
}

// listClusters lists all clusters
func listClusters(cmd *cobra.Command) error {
	// Load configuration
	cfg, err := config.LoadConfig("")
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Listing clusters across all providers...\n\n")

	allClusters := []*ptypes.Cluster{}

	// Query each configured provider
	for providerName, providerCfg := range cfg.Providers {
		p, err := pfactory.DefaultFactory.CreateProvider(providerName, providerCfg.ToProviderMap())
		if err != nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Warning: Failed to create provider %s: %v\n", providerName, err)
			continue
		}

		clusters, err := p.ListClusters(context.Background())
		if err != nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Warning: Failed to list clusters for provider %s: %v\n", providerName, err)
			continue
		}

		allClusters = append(allClusters, clusters...)
	}

	// Always check Kind provider even if not configured (unless already checked)
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
			clusters, err := kindProvider.ListClusters(context.Background())
			if err == nil {
				allClusters = append(allClusters, clusters...)
			}
		}
	}

	if len(allClusters) == 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "No clusters found.\n")
		return nil
	}

	// Print clusters in table format
	fmt.Fprintf(cmd.OutOrStdout(), "%-20s %-10s %-15s %-10s %-15s\n",
		"NAME", "PROVIDER", "REGION", "VERSION", "STATUS")
	fmt.Fprintf(cmd.OutOrStdout(), "%-20s %-10s %-15s %-10s %-15s\n",
		"----", "--------", "------", "-------", "------")

	for _, cluster := range allClusters {
		fmt.Fprintf(cmd.OutOrStdout(), "%-20s %-10s %-15s %-10s %-15s\n",
			cluster.Name, cluster.Provider, cluster.Region, cluster.Version, cluster.Status)
	}

	return nil
}
