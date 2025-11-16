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

var createCmd = &cobra.Command{
	Use:   "create [name]",
	Short: "Create a new Kubernetes cluster",
	Long:  "Create a new production-ready Kubernetes cluster with enterprise features",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return createCluster(cmd, args[0])
	},
}

func init() {
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

	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Creating cluster '%s' with provider '%s'...\n", name, providerName); err != nil {
		return fmt.Errorf("failed to write status: %w", err)
	}

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

	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "✓ Cluster '%s' created successfully!\n", cluster.Name); err != nil {
		return fmt.Errorf("failed to write success message: %w", err)
	}
	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "  ID: %s\n", cluster.ID); err != nil {
		return fmt.Errorf("failed to write cluster ID: %w", err)
	}
	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "  Provider: %s\n", cluster.Provider); err != nil {
		return fmt.Errorf("failed to write provider: %w", err)
	}
	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "  Region: %s\n", cluster.Region); err != nil {
		return fmt.Errorf("failed to write region: %w", err)
	}
	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "  Version: %s\n", cluster.Version); err != nil {
		return fmt.Errorf("failed to write version: %w", err)
	}
	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "  Status: %s\n", cluster.Status); err != nil {
		return fmt.Errorf("failed to write status: %w", err)
	}
	if cluster.Endpoint != "" {
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "  Endpoint: %s\n", cluster.Endpoint); err != nil {
			return fmt.Errorf("failed to write endpoint: %w", err)
		}
	}

	// Automatically setup kubeconfig if requested
	setupKubeconfig, _ := cmd.Flags().GetBool("setup-kubeconfig")
	if setupKubeconfig {
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "\n🔧 Setting up kubeconfig...\n"); err != nil {
			return fmt.Errorf("failed to write kubeconfig message: %w", err)
		}
		err = setupClusterKubeconfig(cmd, cluster, p)
		if err != nil {
			if _, err := fmt.Fprintf(cmd.OutOrStderr(), "⚠️  Warning: Failed to setup kubeconfig: %v\n", err); err != nil {
				return fmt.Errorf("failed to write kubeconfig warning: %w", err)
			}
			if _, err := fmt.Fprintf(cmd.OutOrStderr(), "You can manually setup kubeconfig later with: adhar cluster kubeconfig %s\n", cluster.Name); err != nil {
				return fmt.Errorf("failed to write manual kubeconfig instruction: %w", err)
			}
		} else {
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "✓ Kubeconfig configured successfully!\n"); err != nil {
				return fmt.Errorf("failed to write kubeconfig success: %w", err)
			}

			// Show next steps
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "\n🎉 Cluster is ready! Next steps:\n"); err != nil {
				return fmt.Errorf("failed to write next steps header: %w", err)
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "  • Check cluster status: kubectl get nodes\n"); err != nil {
				return fmt.Errorf("failed to write next steps detail: %w", err)
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "  • Deploy applications: kubectl apply -f your-app.yaml\n"); err != nil {
				return fmt.Errorf("failed to write next steps detail: %w", err)
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "  • View cluster info: kubectl cluster-info\n"); err != nil {
				return fmt.Errorf("failed to write next steps detail: %w", err)
			}

			setCurrentContext, _ := cmd.Flags().GetBool("set-current-context")
			if setCurrentContext {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "  • Current kubectl context set to: %s\n", cluster.Name); err != nil {
					return fmt.Errorf("failed to write context info: %w", err)
				}
			}
		}
	}

	return nil
}
