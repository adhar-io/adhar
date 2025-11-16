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

var deleteCmd = &cobra.Command{
	Use:   "delete [name]",
	Short: "Delete a Kubernetes cluster",
	Long:  "Delete a Kubernetes cluster and all associated resources",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return deleteCluster(cmd, args[0])
	},
}

func init() {
	deleteCmd.Flags().BoolP("force", "f", false, "Force deletion without confirmation")
}

// deleteCluster deletes a cluster
func deleteCluster(cmd *cobra.Command, name string) error {
	fmt.Fprintf(cmd.OutOrStdout(), "Deleting cluster: %s\n", name)

	// Load configuration
	cfg, err := config.LoadConfig("")
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Find the cluster across all providers
	var targetCluster *ptypes.Cluster
	var targetProvider pfactory.Provider
	var targetProviderName string

	for providerName, providerCfg := range cfg.Providers {
		p, err := pfactory.DefaultFactory.CreateProvider(providerName, providerCfg.ToProviderMap())
		if err != nil {
			fmt.Fprintf(cmd.OutOrStderr(), "Warning: Failed to create provider %s: %v\n", providerName, err)
			continue
		}

		// List clusters for this provider
		clusters, err := p.ListClusters(context.Background())
		if err != nil {
			fmt.Fprintf(cmd.OutOrStderr(), "Warning: Failed to list clusters for provider %s: %v\n", providerName, err)
			continue
		}

		// Find cluster by name
		for _, cluster := range clusters {
			if cluster.Name == name {
				targetCluster = cluster
				targetProvider = p
				targetProviderName = providerName
				break
			}
		}

		if targetCluster != nil {
			break
		}
	}

	// Also check Kind provider even if not configured (unless already checked)
	if targetCluster == nil {
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
					for _, cluster := range clusters {
						if cluster.Name == name {
							targetCluster = cluster
							targetProvider = kindProvider
							targetProviderName = "kind"
							break
						}
					}
				}
			}
		}
	}

	if targetCluster == nil {
		return fmt.Errorf("cluster '%s' not found in any configured provider", name)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Found cluster '%s' in provider '%s'\n", name, targetProviderName)
	fmt.Fprintf(cmd.OutOrStdout(), "  ID: %s\n", targetCluster.ID)
	fmt.Fprintf(cmd.OutOrStdout(), "  Status: %s\n", targetCluster.Status)

	// Check if cluster is managed by Adhar
	isAdharManaged := false
	if targetCluster.Tags != nil {
		if managedBy, exists := targetCluster.Tags["adhar.io/managed-by"]; exists && managedBy == "adhar" {
			isAdharManaged = true
		}
	}

	if !isAdharManaged {
		fmt.Fprintf(cmd.OutOrStdout(), "⚠️  Warning: This cluster was not created by Adhar (missing adhar.io/managed-by tag)\n")
		fmt.Fprintf(cmd.OutOrStdout(), "Proceeding with deletion anyway...\n")
	}

	// Check for force flag
	force, _ := cmd.Flags().GetBool("force")

	// Confirm deletion unless force flag is used
	if !force {
		fmt.Fprintf(cmd.OutOrStdout(), "\n🗑️  This action will permanently delete the cluster and all associated resources.\n")
		fmt.Fprintf(cmd.OutOrStdout(), "Type 'yes' to confirm deletion: ")

		var confirmation string
		fmt.Scanln(&confirmation)

		if confirmation != "yes" {
			fmt.Fprintf(cmd.OutOrStdout(), "Deletion cancelled.\n")
			return nil
		}
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "\n🗑️  Force deletion enabled - proceeding without confirmation.\n")
	}

	// Start deletion process
	fmt.Fprintf(cmd.OutOrStdout(), "\n🚀 Starting cluster deletion...\n")

	// Set cluster status to deleting if possible
	ctx := context.Background()

	// Delete the cluster using the provider
	err = targetProvider.DeleteCluster(ctx, targetCluster.ID)
	if err != nil {
		return fmt.Errorf("failed to delete cluster: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "✅ Cluster '%s' deletion initiated successfully!\n", name)
	fmt.Fprintf(cmd.OutOrStdout(), "\nNote: It may take several minutes for all resources to be fully deleted.\n")
	fmt.Fprintf(cmd.OutOrStdout(), "You can check the status with: adhar cluster list\n")

	return nil
}
