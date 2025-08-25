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
	pfactory "adhar-io/adhar/platform/providers"
	ptypes "adhar-io/adhar/platform/types"
)

var kubeconfigCmd = &cobra.Command{
	Use:   "kubeconfig [name]",
	Short: "Get and setup cluster kubeconfig",
	Long:  "Download and configure kubeconfig for accessing a Kubernetes cluster",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return getAndSetupKubeconfig(cmd, args[0])
	},
}

func init() {
	kubeconfigCmd.Flags().StringP("output", "o", "", "Output kubeconfig to file (default: merge with ~/.kube/config)")
	kubeconfigCmd.Flags().BoolP("set-current-context", "", false, "Set the cluster as current kubectl context")
	kubeconfigCmd.Flags().BoolP("print-only", "", false, "Print kubeconfig to stdout instead of saving")
}

// getAndSetupKubeconfig downloads and sets up kubeconfig for a cluster
func getAndSetupKubeconfig(cmd *cobra.Command, clusterName string) error {
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
			if cluster.Name == clusterName {
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

	if targetCluster == nil {
		return fmt.Errorf("cluster '%s' not found in any configured provider", clusterName)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "📍 Found cluster '%s' in provider '%s'\n", clusterName, targetProviderName)
	fmt.Fprintf(cmd.OutOrStdout(), "   ID: %s\n", targetCluster.ID)
	fmt.Fprintf(cmd.OutOrStdout(), "   Status: %s\n", targetCluster.Status)

	// Get kubeconfig from provider
	ctx := context.Background()
	kubeconfig, err := targetProvider.GetKubeconfig(ctx, targetCluster.ID)
	if err != nil {
		return fmt.Errorf("failed to get kubeconfig: %w", err)
	}

	// Check if user wants to print only
	printOnly, _ := cmd.Flags().GetBool("print-only")
	if printOnly {
		fmt.Fprintf(cmd.OutOrStdout(), "\n# Kubeconfig for cluster: %s\n", clusterName)
		fmt.Fprintf(cmd.OutOrStdout(), "%s\n", kubeconfig)
		return nil
	}

	// Create kubeconfig manager
	outputPath, _ := cmd.Flags().GetString("output")
	manager := helpers.NewKubeconfigManager(outputPath)

	// Create backup if existing config exists
	fmt.Fprintf(cmd.OutOrStdout(), "\n🔧 Setting up kubeconfig...\n")
	backupPath, err := manager.BackupKubeconfig()
	if err != nil {
		fmt.Fprintf(cmd.OutOrStderr(), "⚠️  Warning: Failed to backup existing kubeconfig: %v\n", err)
	} else if backupPath != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "✓ Existing kubeconfig backed up to: %s\n", backupPath)
	}

	// Merge the new kubeconfig
	err = manager.MergeKubeconfig(kubeconfig, clusterName)
	if err != nil {
		return fmt.Errorf("failed to merge kubeconfig: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "✓ Kubeconfig updated successfully\n")

	// Set current context if requested
	setCurrentContext, _ := cmd.Flags().GetBool("set-current-context")
	if setCurrentContext {
		err = manager.SetCurrentContext(clusterName)
		if err != nil {
			fmt.Fprintf(cmd.OutOrStderr(), "⚠️  Warning: Failed to set current context: %v\n", err)
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "✓ Current context set to: %s\n", clusterName)
		}
	}

	// Validate the kubeconfig
	err = manager.ValidateKubeconfig()
	if err != nil {
		fmt.Fprintf(cmd.OutOrStderr(), "⚠️  Warning: Kubeconfig validation failed: %v\n", err)
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "✓ Kubeconfig validation passed\n")
	}

	// Show available contexts
	contexts, err := manager.ListContexts()
	if err == nil && len(contexts) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "\n📋 Available contexts:\n")
		currentContext, _ := manager.GetCurrentContext()
		for _, ctx := range contexts {
			if ctx == currentContext {
				fmt.Fprintf(cmd.OutOrStdout(), "  • %s (current)\n", ctx)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "  • %s\n", ctx)
			}
		}
	}

	fmt.Fprintf(cmd.OutOrStdout(), "\n🎉 Kubeconfig setup complete! You can now use:\n")
	fmt.Fprintf(cmd.OutOrStdout(), "  • kubectl get nodes\n")
	fmt.Fprintf(cmd.OutOrStdout(), "  • kubectl cluster-info\n")
	fmt.Fprintf(cmd.OutOrStdout(), "  • kubectl get pods --all-namespaces\n")

	return nil
}
