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

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/config"
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
	// -f is --file, matching `adhar up`, `adhar down` and the other cluster
	// subcommands. It used to mean --force here alone, so the muscle-memory
	// `adhar cluster delete dev -f config.yaml` set force and left the config
	// unread. --force keeps its long form.
	deleteCmd.Flags().Bool("force", false, "Force deletion without confirmation")
	deleteCmd.Flags().StringP("file", "f", "", "Path to configuration file")
	deleteCmd.Flags().Bool("purge-orphaned-volumes", false,
		"Also delete unattached pvc-* block-storage volumes in the cluster's region that carry no other cluster's tag "+
			"(volumes left behind by clusters created before per-cluster volume tagging). Only safe when no other Kubernetes cluster uses that region.")
}

// deleteCluster deletes a cluster
func deleteCluster(cmd *cobra.Command, name string) error {
	fmt.Fprintf(cmd.OutOrStdout(), "Deleting cluster: %s\n", name)

	configFile, _ := cmd.Flags().GetString("file")
	cfg, err := config.LoadConfig(configFile)
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	providerOpts := map[string]interface{}{}
	if purge, _ := cmd.Flags().GetBool("purge-orphaned-volumes"); purge {
		providerOpts["purgeOrphanedVolumes"] = true
	}

	// One shared lookup for `adhar down`, `adhar cluster delete` and
	// `adhar cluster status`, so a cluster name always resolves the same way.
	// These each had their own copy and they disagreed.
	ctx := context.Background()
	found, err := helpers.FindCluster(ctx, cfg, name, providerOpts, func(w string) {
		fmt.Fprintf(cmd.OutOrStderr(), "Warning: %s\n", w)
	})
	if err != nil {
		return err
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Found cluster '%s' in provider '%s'\n", name, found.ProviderName)
	fmt.Fprintf(cmd.OutOrStdout(), "  ID: %s\n", found.Cluster.ID)
	fmt.Fprintf(cmd.OutOrStdout(), "  Status: %s\n", found.Cluster.Status)

	if !found.IsAdharManaged() {
		fmt.Fprintf(cmd.OutOrStdout(), "⚠️  Warning: This cluster was not created by Adhar (missing adhar.io/managed-by tag)\n")
		fmt.Fprintf(cmd.OutOrStdout(), "Proceeding with deletion anyway...\n")
	}

	force, _ := cmd.Flags().GetBool("force")
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

	fmt.Fprintf(cmd.OutOrStdout(), "\n🚀 Starting cluster deletion...\n")

	if err := found.Provider.DeleteCluster(ctx, found.Cluster.ID); err != nil {
		return fmt.Errorf("failed to delete cluster: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "✅ Cluster '%s' deletion initiated successfully!\n", name)
	fmt.Fprintf(cmd.OutOrStdout(), "\nNote: It may take several minutes for all resources to be fully deleted.\n")
	fmt.Fprintf(cmd.OutOrStdout(), "You can check the status with: adhar cluster list --file %s\n", configFile)

	return nil
}
