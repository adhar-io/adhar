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

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/config"
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

func init() {
	// Without this the command loaded the DEFAULT config, which names no cloud
	// provider, so it queried nothing and reported every cloud cluster as not
	// found -- the same defect `adhar cluster list` had.
	statusCmd.Flags().StringP("file", "f", "", "Path to configuration file")
}

// getClusterStatus gets detailed cluster status
func getClusterStatus(cmd *cobra.Command, name string) error {
	fmt.Fprintf(cmd.OutOrStdout(), "Getting status for cluster: %s\n", name)

	configFile, _ := cmd.Flags().GetString("file")
	cfg, err := config.LoadConfig(configFile)
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Resolve through the SAME lookup `adhar down` and `adhar cluster delete`
	// use, so all three agree on which cluster a name refers to. The previous
	// code synthesised an id ("<provider>-<name>") that no provider actually
	// registers, and skipped every error silently -- expired credentials were
	// indistinguishable from a missing cluster.
	ctx := context.Background()
	found, err := helpers.FindCluster(ctx, cfg, name, nil, func(w string) {
		fmt.Fprintf(cmd.OutOrStderr(), "Warning: %s\n", w)
	})
	if err != nil {
		return err
	}

	p := found.Provider
	cluster := found.Cluster

	fmt.Fprintf(cmd.OutOrStdout(), "\nCluster Information:\n")
	fmt.Fprintf(cmd.OutOrStdout(), "  Name: %s\n", cluster.Name)
	fmt.Fprintf(cmd.OutOrStdout(), "  ID: %s\n", cluster.ID)
	fmt.Fprintf(cmd.OutOrStdout(), "  Provider: %s\n", found.ProviderName)
	fmt.Fprintf(cmd.OutOrStdout(), "  Region: %s\n", cluster.Region)
	fmt.Fprintf(cmd.OutOrStdout(), "  Version: %s\n", cluster.Version)
	fmt.Fprintf(cmd.OutOrStdout(), "  Status: %s\n", cluster.Status)
	if cluster.Endpoint != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "  Endpoint: %s\n", cluster.Endpoint)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "  Created: %s\n", cluster.CreatedAt.Format(time.RFC3339))
	fmt.Fprintf(cmd.OutOrStdout(), "  Updated: %s\n", cluster.UpdatedAt.Format(time.RFC3339))

	// Health and metrics are best-effort: a provider that cannot report them
	// should not hide the cluster information above, but it must say so.
	if health, err := p.GetClusterHealth(ctx, cluster.ID); err != nil {
		fmt.Fprintf(cmd.OutOrStderr(), "\nHealth Status: unavailable (%v)\n", err)
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "\nHealth Status: %s\n", health.Status)
		for component, componentHealth := range health.Components {
			fmt.Fprintf(cmd.OutOrStdout(), "  %s: %s\n", component, componentHealth.Status)
		}
	}

	if metrics, err := p.GetClusterMetrics(ctx, cluster.ID); err != nil {
		fmt.Fprintf(cmd.OutOrStderr(), "Resource Usage: unavailable (%v)\n", err)
	} else {
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
