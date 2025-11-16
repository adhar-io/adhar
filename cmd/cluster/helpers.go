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
	pfactory "adhar-io/adhar/platform/providers"
	ptypes "adhar-io/adhar/platform/types"
)

func writeStdout(cmd *cobra.Command, format string, args ...interface{}) {
	if _, err := fmt.Fprintf(cmd.OutOrStdout(), format, args...); err != nil {
		cmd.PrintErrf("output error: %v\n", err)
	}
}

func writeStderr(cmd *cobra.Command, format string, args ...interface{}) {
	if _, err := fmt.Fprintf(cmd.OutOrStderr(), format, args...); err != nil {
		cmd.PrintErrf("output error: %v\n", err)
	}
}

// setupClusterKubeconfig automatically downloads and configures kubeconfig for the cluster
func setupClusterKubeconfig(cmd *cobra.Command, cluster *ptypes.Cluster, provider pfactory.Provider) error {
	ctx := context.Background()

	// Get kubeconfig from the provider
	kubeconfig, err := provider.GetKubeconfig(ctx, cluster.ID)
	if err != nil {
		return fmt.Errorf("failed to get kubeconfig from provider: %w", err)
	}

	// Determine kubeconfig path
	kubeconfigPath, _ := cmd.Flags().GetString("kubeconfig-path")

	// Create kubeconfig manager
	manager := helpers.NewKubeconfigManager(kubeconfigPath)

	// Create backup if existing config exists
	backupPath, err := manager.BackupKubeconfig()
	if err != nil {
		writeStderr(cmd, "  ⚠️  Warning: Failed to backup existing kubeconfig: %v\n", err)
	} else if backupPath != "" {
		writeStdout(cmd, "  • Existing kubeconfig backed up to: %s\n", backupPath)
	}

	// Merge the new kubeconfig
	err = manager.MergeKubeconfig(kubeconfig, cluster.Name)
	if err != nil {
		return fmt.Errorf("failed to merge kubeconfig: %w", err)
	}

	writeStdout(cmd, "  • Kubeconfig updated successfully\n")

	// Always set current context for the new cluster
	err = manager.SetCurrentContext(cluster.Name)
	if err != nil {
		writeStderr(cmd, "  ⚠️  Warning: Failed to set current context: %v\n", err)
	} else {
		writeStdout(cmd, "  • Current context set to: %s\n", cluster.Name)
	}

	// Validate the kubeconfig
	err = manager.ValidateKubeconfig()
	if err != nil {
		writeStderr(cmd, "  ⚠️  Warning: Kubeconfig validation failed: %v\n", err)
	} else {
		writeStdout(cmd, "  • Kubeconfig validation passed\n")
	}

	// Provide helpful next steps
	writeStdout(cmd, "  • You can now run: kubectl get nodes\n")
	writeStdout(cmd, "  • To switch contexts: kubectl config use-context %s\n", cluster.Name)

	return nil
}
