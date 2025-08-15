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

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/config"
	pfactory "adhar-io/adhar/platform/providers"
	ptypes "adhar-io/adhar/platform/types"

	// Import providers to register them
	_ "adhar-io/adhar/platform/providers/aws"
	_ "adhar-io/adhar/platform/providers/azure"
	_ "adhar-io/adhar/platform/providers/civo"
	_ "adhar-io/adhar/platform/providers/custom"
	_ "adhar-io/adhar/platform/providers/digitalocean"
	_ "adhar-io/adhar/platform/providers/gcp"
	_ "adhar-io/adhar/platform/providers/kind"
)

// newClusterCommandV2 creates an updated cluster command with actual functionality
func newClusterCommandV2() *cobra.Command {
	clusterCmd := &cobra.Command{
		Use:   "cluster",
		Short: "Manage Kubernetes clusters",
		Long:  "Create, manage, and operate Kubernetes clusters across multiple cloud providers",
	}

	createCmd := &cobra.Command{
		Use:   "create [name]",
		Short: "Create a new Kubernetes cluster",
		Long:  "Create a new production-ready Kubernetes cluster with enterprise features",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return createCluster(cmd, args[0])
		},
	}
	createCmd.Flags().StringP("provider", "p", "kind", "Cloud provider to use")
	createCmd.Flags().StringP("region", "r", "local", "Provider region")
	createCmd.Flags().StringP("version", "", "v1.29.0", "Kubernetes version")
	createCmd.Flags().IntP("control-plane-replicas", "", 1, "Number of control plane nodes")
	createCmd.Flags().IntP("worker-replicas", "", 3, "Number of worker nodes")
	createCmd.Flags().StringP("instance-type", "", "m5.large", "Instance type for nodes")
	createCmd.Flags().StringP("file", "f", "", "Path to configuration file")
	createCmd.Flags().BoolP("setup-kubeconfig", "", true, "Automatically setup kubeconfig after cluster creation")
	createCmd.Flags().StringP("kubeconfig-path", "", "", "Custom path for kubeconfig (default: ~/.kube/config)")
	createCmd.Flags().BoolP("set-current-context", "", true, "Set the new cluster as current kubectl context")

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List all clusters",
		Long:  "List all Kubernetes clusters across all configured providers",
		RunE: func(cmd *cobra.Command, args []string) error {
			return listClusters(cmd)
		},
	}

	statusCmd := &cobra.Command{
		Use:   "status [name]",
		Short: "Get cluster status",
		Long:  "Get detailed status information about a Kubernetes cluster",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return getClusterStatus(cmd, args[0])
		},
	}

	deleteCmd := &cobra.Command{
		Use:   "delete [name]",
		Short: "Delete a Kubernetes cluster",
		Long:  "Delete a Kubernetes cluster and all associated resources",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return deleteCluster(cmd, args[0])
		},
	}
	deleteCmd.Flags().BoolP("force", "f", false, "Force deletion without confirmation")

	kubeconfigCmd := &cobra.Command{
		Use:   "kubeconfig [name]",
		Short: "Get and setup cluster kubeconfig",
		Long:  "Download and configure kubeconfig for accessing a Kubernetes cluster",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return getAndSetupKubeconfig(cmd, args[0])
		},
	}
	kubeconfigCmd.Flags().StringP("output", "o", "", "Output kubeconfig to file (default: merge with ~/.kube/config)")
	kubeconfigCmd.Flags().BoolP("set-current-context", "", false, "Set the cluster as current kubectl context")
	kubeconfigCmd.Flags().BoolP("print-only", "", false, "Print kubeconfig to stdout instead of saving")

	investigateCmd := &cobra.Command{
		Use:   "investigate [name]",
		Short: "Investigate cluster connectivity issues",
		Long:  "Perform comprehensive investigation of cluster connectivity and setup issues",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return investigateCluster(cmd, args[0])
		},
	}

	clusterCmd.AddCommand(createCmd)
	clusterCmd.AddCommand(listCmd)
	clusterCmd.AddCommand(statusCmd)
	clusterCmd.AddCommand(deleteCmd)
	clusterCmd.AddCommand(kubeconfigCmd)
	clusterCmd.AddCommand(investigateCmd)

	debugCmd := &cobra.Command{
		Use:   "debug [name]",
		Short: "Debug a cluster instance",
		Long:  "Provides SSH command to connect to a cluster's master node for debugging.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return debugCluster(cmd, args[0])
		},
	}
	clusterCmd.AddCommand(debugCmd)

	return clusterCmd
}

// createCluster creates a new Kubernetes cluster
func createCluster(cmd *cobra.Command, name string) error {
	configFile, _ := cmd.Flags().GetString("file")

	// Load configuration first
	cfg, err := config.LoadConfig(configFile)
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Determine provider: CLI flag > primary provider from config > default
	providerName, _ := cmd.Flags().GetString("provider")
	providerSpecifiedViaFlag := cmd.Flags().Changed("provider")

	if !providerSpecifiedViaFlag {
		// No provider specified via CLI, find primary provider from config
		for name, providerCfg := range cfg.Providers {
			if providerCfg.Primary {
				providerName = name
				break
			}
		}
		// If no primary provider found, use the first available provider
		if providerName == "" || providerName == "kind" {
			for name := range cfg.Providers {
				if name != "kind" {
					providerName = name
					break
				}
			}
		}
	}

	// Get other settings: CLI flags > hardcoded defaults
	region, _ := cmd.Flags().GetString("region")
	if !cmd.Flags().Changed("region") {
		// Use provider's default region if available
		if provider, exists := cfg.Providers[providerName]; exists {
			region = provider.Region
		}
	}

	version, _ := cmd.Flags().GetString("version")
	controlPlaneReplicas, _ := cmd.Flags().GetInt("control-plane-replicas")
	workerReplicas, _ := cmd.Flags().GetInt("worker-replicas")

	instanceType, _ := cmd.Flags().GetString("instance-type")
	// Use default instance type based on provider
	if !cmd.Flags().Changed("instance-type") {
		switch providerName {
		case "aws":
			instanceType = "t3.medium"
		case "gcp":
			instanceType = "e2-medium"
		case "azure":
			instanceType = "Standard_B2s"
		case "digitalocean":
			instanceType = "s-2vcpu-2gb"
		case "civo":
			instanceType = "g3.medium"
		default:
			instanceType = "s-1vcpu-2gb" // Default to DigitalOcean basic size
		}
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Creating cluster '%s' with provider '%s'...\n", name, providerName)

	// Get provider config
	providerCfg, exists := cfg.Providers[providerName]
	if !exists {
		if providerName == "kind" {
			// Use default Kind config
			providerCfg = config.ConfigProviderConfig{
				Type:   "kind",
				Region: "local",
				Config: map[string]interface{}{
					"kindPath":    "kind",
					"kubectlPath": "kubectl",
				},
			}
		} else {
			return fmt.Errorf("provider '%s' is not configured in the config file", providerName)
		}
	}

	// Use provider's region if not specified via CLI and not set from defaults
	if !cmd.Flags().Changed("region") && region == "local" && providerCfg.Region != "" {
		region = providerCfg.Region
	}

	// Override provider config region with CLI region if specified
	if cmd.Flags().Changed("region") {
		providerCfg.Region = region
	}

	// Create provider instance
	p, err := pfactory.DefaultFactory.CreateProvider(providerName, providerCfg.ToProviderMap())
	if err != nil {
		return fmt.Errorf("failed to create provider: %w", err)
	}

	// Create cluster specification
	spec := &ptypes.ClusterSpec{
		TypeMeta: ptypes.TypeMeta{
			Kind:       "ClusterSpec",
			APIVersion: "adhar.io/v1alpha1",
		},
		ObjectMeta: ptypes.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				"adhar.io/managed-by":   "adhar",
				"adhar.io/cluster-name": name,
				"adhar.io/provider":     providerName,
				"adhar.io/version":      "v1.0.0",
				"adhar.io/created-at":   time.Now().Format(time.RFC3339),
			},
		},
		Provider: providerName,
		Region:   region,
		Version:  version,
		ControlPlane: ptypes.ControlPlaneSpec{
			Replicas:         controlPlaneReplicas,
			InstanceType:     instanceType,
			HighAvailability: controlPlaneReplicas > 1,
		},
		NodeGroups: []ptypes.NodeGroupSpec{
			{
				Name:         "workers",
				Replicas:     workerReplicas,
				InstanceType: instanceType,
				Labels: map[string]string{
					"adhar.io/managed-by":   "adhar",
					"adhar.io/cluster-name": name,
					"adhar.io/nodegroup":    "workers",
				},
			},
		},
		Networking: ptypes.NetworkingSpec{
			CNI:         "cilium",
			PodCIDR:     "10.244.0.0/16",
			ServiceCIDR: "10.96.0.0/16",
			ClusterDNS:  "coredns",
		},
		Tags: map[string]string{
			"adhar.io/managed-by":   "adhar",
			"adhar.io/cluster-name": name,
			"adhar.io/provider":     providerName,
			"adhar.io/created-by":   "adhar-cli",
			"adhar.io/version":      "v1.0.0",
		},
		Security: ptypes.SecuritySpec{
			RBAC:                 true,
			NetworkPolicies:      true,
			PodSecurityStandards: "restricted",
		},
		Addons: ptypes.AddonsSpec{
			Monitoring: ptypes.MonitoringSpec{
				Prometheus: true,
			},
			Ingress: ptypes.IngressSpec{
				NGINX: true,
			},
		},
		Domain: nil, // Domain configuration not available in simplified config
	}

	// Create the cluster with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cluster, err := p.CreateCluster(ctx, spec)
	if err != nil {
		return fmt.Errorf("failed to create cluster: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "✓ Cluster '%s' created successfully!\n", cluster.Name)
	fmt.Fprintf(cmd.OutOrStdout(), "  ID: %s\n", cluster.ID)
	fmt.Fprintf(cmd.OutOrStdout(), "  Provider: %s\n", cluster.Provider)
	fmt.Fprintf(cmd.OutOrStdout(), "  Region: %s\n", cluster.Region)
	fmt.Fprintf(cmd.OutOrStdout(), "  Version: %s\n", cluster.Version)
	fmt.Fprintf(cmd.OutOrStdout(), "  Status: %s\n", cluster.Status)
	if cluster.Endpoint != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "  Endpoint: %s\n", cluster.Endpoint)
	}

	// Automatically setup kubeconfig if requested
	setupKubeconfig, _ := cmd.Flags().GetBool("setup-kubeconfig")
	if setupKubeconfig {
		fmt.Fprintf(cmd.OutOrStdout(), "\n🔧 Setting up kubeconfig...\n")
		err = setupClusterKubeconfig(cmd, cluster, p)
		if err != nil {
			fmt.Fprintf(cmd.OutOrStderr(), "⚠️  Warning: Failed to setup kubeconfig: %v\n", err)
			fmt.Fprintf(cmd.OutOrStderr(), "You can manually setup kubeconfig later with: adhar cluster kubeconfig %s\n", cluster.Name)
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "✓ Kubeconfig configured successfully!\n")

			// Show next steps
			fmt.Fprintf(cmd.OutOrStdout(), "\n🎉 Cluster is ready! Next steps:\n")
			fmt.Fprintf(cmd.OutOrStdout(), "  • Check cluster status: kubectl get nodes\n")
			fmt.Fprintf(cmd.OutOrStdout(), "  • Deploy applications: kubectl apply -f your-app.yaml\n")
			fmt.Fprintf(cmd.OutOrStdout(), "  • View cluster info: kubectl cluster-info\n")

			setCurrentContext, _ := cmd.Flags().GetBool("set-current-context")
			if setCurrentContext {
				fmt.Fprintf(cmd.OutOrStdout(), "  • Current kubectl context set to: %s\n", cluster.Name)
			}
		}
	}

	return nil
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
		fmt.Fprintf(cmd.OutOrStderr(), "  ⚠️  Warning: Failed to backup existing kubeconfig: %v\n", err)
	} else if backupPath != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "  • Existing kubeconfig backed up to: %s\n", backupPath)
	}

	// Merge the new kubeconfig
	err = manager.MergeKubeconfig(kubeconfig, cluster.Name)
	if err != nil {
		return fmt.Errorf("failed to merge kubeconfig: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "  • Kubeconfig updated successfully\n")

	// Always set current context for the new cluster
	err = manager.SetCurrentContext(cluster.Name)
	if err != nil {
		fmt.Fprintf(cmd.OutOrStderr(), "  ⚠️  Warning: Failed to set current context: %v\n", err)
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "  • Current context set to: %s\n", cluster.Name)
	}

	// Validate the kubeconfig
	err = manager.ValidateKubeconfig()
	if err != nil {
		fmt.Fprintf(cmd.OutOrStderr(), "  ⚠️  Warning: Kubeconfig validation failed: %v\n", err)
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "  • Kubeconfig validation passed\n")
	}

	// Provide helpful next steps
	fmt.Fprintf(cmd.OutOrStdout(), "  • You can now run: kubectl get nodes\n")
	fmt.Fprintf(cmd.OutOrStdout(), "  • To switch contexts: kubectl config use-context %s\n", cluster.Name)

	return nil
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

func debugCluster(cmd *cobra.Command, clusterName string) error {
	fmt.Printf("🔍 Attempting to debug cluster: %s\n", clusterName)

	// This is a Civo-specific debug implementation for now.
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("could not get user home directory: %w", err)
	}

	// The private key for the first master node is saved with a predictable name.
	masterKeyName := fmt.Sprintf("%s-master-0.pem", clusterName)
	keyPath := filepath.Join(homeDir, ".adhar", "keys", masterKeyName)

	if _, err := os.Stat(keyPath); os.IsNotExist(err) {
		return fmt.Errorf("private key for cluster '%s' not found at '%s'. Please run the cluster creation again to generate the key.", clusterName, keyPath)
	}

	fmt.Printf("🔑 Private key found: %s\n", keyPath)

	// Now, we need to find the public IP of the master node.
	cfg, err := config.LoadConfig("")
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	providerConfig, ok := cfg.Providers["civo"]
	if !ok {
		return fmt.Errorf("civo provider not found in configuration")
	}
	prov, err := pfactory.DefaultFactory.CreateProvider("civo", providerConfig.ToProviderMap())
	if err != nil {
		return fmt.Errorf("failed to create civo provider: %w", err)
	}

	clusters, err := prov.ListClusters(context.Background())
	if err != nil {
		return fmt.Errorf("failed to list clusters: %w", err)
	}

	var masterIP string
	for _, cluster := range clusters {
		if cluster.Name == clusterName {
			masterIP = strings.TrimPrefix(cluster.Endpoint, "https://")
			masterIP = strings.TrimSuffix(masterIP, ":6443")
			break
		}
	}

	if masterIP == "" {
		return fmt.Errorf("could not find a public IP for the master node of cluster '%s'. Is the cluster still running?", clusterName)
	}

	fmt.Printf("🖥️ Master node IP found: %s\n", masterIP)
	fmt.Printf("\nTo connect to the master node, run the following command in your terminal:\n\n")
	fmt.Printf("ssh -i %s root@%s\n\n", keyPath, masterIP)
	fmt.Printf("Once connected, you can check the setup log with:\n\n")
	fmt.Printf("tail -f /var/log/k8s-setup.log\n\n")

	return nil
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
