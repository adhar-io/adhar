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

var investigateCmd = &cobra.Command{
	Use:   "investigate [name]",
	Short: "Investigate cluster connectivity issues",
	Long:  "Perform comprehensive investigation of cluster connectivity and setup issues",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return investigateCluster(cmd, args[0])
	},
}

func init() {
	investigateCmd.Flags().StringP("file", "f", "", "Path to configuration file")
}

func investigateCluster(cmd *cobra.Command, clusterName string) error {
	fmt.Printf("🔍 Investigating cluster: %s\n", clusterName)

	// Load configuration
	configFile, _ := cmd.Flags().GetString("file")
	cfg, err := config.LoadConfig(configFile)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Find the cluster across all providers
	var foundCluster *ptypes.Cluster
	var foundProvider pfactory.Provider

	// Try to find the cluster in each provider
	for providerName, providerConfig := range cfg.Providers {
		prov, err := pfactory.DefaultFactory.CreateProvider(providerName, providerConfig.ToProviderMap())
		if err != nil {
			fmt.Printf("⚠️  Warning: failed to create provider %s: %v\n", providerName, err)
			continue
		}

		// List clusters in this provider
		clusters, err := prov.ListClusters(context.Background())
		if err != nil {
			fmt.Printf("⚠️  Warning: failed to list clusters in provider %s: %v\n", providerName, err)
			continue
		}

		// Look for the cluster
		for _, cluster := range clusters {
			if cluster.Name == clusterName || cluster.ID == clusterName {
				foundCluster = cluster
				foundProvider = prov
				break
			}
		}

		if foundCluster != nil {
			break
		}
	}

	if foundCluster == nil {
		return fmt.Errorf("cluster '%s' not found in any configured provider", clusterName)
	}

	fmt.Printf("📍 Found cluster '%s' in provider '%s'\n", foundCluster.Name, foundCluster.Provider)
	fmt.Printf("   ID: %s\n", foundCluster.ID)
	fmt.Printf("   Status: %s\n", foundCluster.Status)
	fmt.Printf("   Region: %s\n", foundCluster.Region)

	// Perform investigation
	fmt.Printf("\n🔍 Starting comprehensive investigation...\n")
	err = foundProvider.InvestigateCluster(context.Background(), foundCluster.ID)
	if err != nil {
		fmt.Printf("❌ Investigation failed: %v\n", err)
		return err
	}
	fmt.Printf("✅ Investigation completed\n")

	return nil
}
