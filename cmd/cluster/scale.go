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
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/config"
	pfactory "adhar-io/adhar/platform/providers"
)

var scaleCmd = &cobra.Command{
	Use:   "scale [name]",
	Short: "Scale a cluster's worker nodes",
	Long: `Scale the worker node count of a cluster.

Scale-up provisions new instances and joins them with kubeadm; scale-down
drains nodes, removes them from the cluster, then deletes the instances.

The provider is built the way 'adhar up' built it for the cluster's environment
(its region, clusterConfig and batch pool), so '--node-group batch' on a pool
that has lost every node adds nodes of the pool's machineType. The environment
is --env, or the one environment on the provider whose cluster has this name.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return scaleCluster(cmd, args[0])
	},
}

func init() {
	scaleCmd.Flags().Int("workers", -1, "Desired worker node count")
	scaleCmd.Flags().String("node-group", "workers", "Node group to scale")
	scaleCmd.Flags().StringP("provider", "p", "", "Provider hosting the cluster (searched across all configured providers when omitted)")
	scaleCmd.Flags().StringP("file", "f", "", "Path to configuration file")
	scaleCmd.Flags().StringP("env", "e", "", "Environment the cluster belongs to (found by cluster name when omitted)")
	_ = scaleCmd.MarkFlagRequired("workers")
}

func scaleCluster(cmd *cobra.Command, name string) error {
	workers, _ := cmd.Flags().GetInt("workers")
	if workers < 0 {
		return fmt.Errorf("--workers must be >= 0")
	}
	nodeGroup, _ := cmd.Flags().GetString("node-group")
	providerFlag, _ := cmd.Flags().GetString("provider")
	configFile, _ := cmd.Flags().GetString("file")
	envFlag, _ := cmd.Flags().GetString("env")

	cfg, err := config.LoadConfig(configFile)
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	env, err := scaleEnvironment(cfg, envFlag, providerFlag, name)
	if err != nil {
		return err
	}
	if env != nil && providerFlag == "" {
		providerFlag = env.ResolvedProvider
	}
	if nodeGroup == globals.BatchNodeGroupName && pfactory.BatchPoolMachineType(env) == "" {
		fmt.Fprintf(cmd.OutOrStderr(), "Warning: no environment with a batchPool.machineType was matched (pass --env); "+
			"if the batch pool has no node left, new ones take the worker size\n")
	}

	prov, clusterID, err := resolveClusterProvider(cmd, cfg, name, providerFlag, env)
	if err != nil {
		return err
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Scaling cluster %s node group %q to %d workers...\n", name, nodeGroup, workers)
	if err := prov.ScaleNodeGroup(context.Background(), clusterID, nodeGroup, workers); err != nil {
		return fmt.Errorf("failed to scale cluster %s: %w", name, err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "● Cluster %s scaled to %d workers\n", name, workers)
	return nil
}

// resolveClusterProvider locates the cluster by name, either in the named
// provider or across all configured providers, returning the provider
// instance and the cluster's provider-native ID.
func resolveClusterProvider(cmd *cobra.Command, cfg *config.Config, name, providerFlag string, env *config.ResolvedEnvironmentConfig) (pfactory.Provider, string, error) {
	providers := cfg.Providers
	if providerFlag != "" {
		pc, ok := cfg.Providers[providerFlag]
		if !ok {
			return nil, "", fmt.Errorf("provider %q is not configured", providerFlag)
		}
		providers = map[string]config.ConfigProviderConfig{providerFlag: pc}
	}

	for providerName, providerCfg := range providers {
		p, err := createScaleProvider(providerName, scaleProviderMap(providerName, providerCfg, env))
		if err != nil {
			fmt.Fprintf(cmd.OutOrStderr(), "Warning: failed to create provider %s: %v\n", providerName, err)
			continue
		}
		clusters, err := p.ListClusters(context.Background())
		if err != nil {
			fmt.Fprintf(cmd.OutOrStderr(), "Warning: failed to list clusters for provider %s: %v\n", providerName, err)
			continue
		}
		for _, c := range clusters {
			if c.Name == name || c.ID == name {
				return p, c.ID, nil
			}
		}
	}
	return nil, "", fmt.Errorf("cluster %q not found in any configured provider", name)
}

// createScaleProvider builds the provider a scale runs through; a variable only so
// tests can see the map it is built from without a cloud.
var createScaleProvider = pfactory.DefaultFactory.CreateProvider

// scaleProviderMap is the map the provider is built from: the provider block and,
// when the cluster's environment lives on this provider, the same environment
// layer `adhar up` applies (pfactory.EnvironmentProviderOverrides: its region, its
// clusterConfig, and the batch pool's machine type, which a scale path needs when
// the pool it grows has no node left to copy a size from).
func scaleProviderMap(providerName string, providerCfg config.ConfigProviderConfig, env *config.ResolvedEnvironmentConfig) map[string]interface{} {
	m := providerCfg.ToProviderMap()
	if env != nil && env.ResolvedProvider == providerName {
		for k, v := range pfactory.EnvironmentProviderOverrides(env) {
			m[k] = v
		}
	}
	return m
}

// scaleEnvironment is the environment whose cluster is being scaled: the named
// one (--env, an error when it does not exist), else the ONE environment on the
// provider (--provider, or any) whose cluster carries this name, as `adhar up`
// names it (helpers.EnvironmentClusterNames). nil when none or several match: the
// scale then runs on the provider block alone, as it always did.
func scaleEnvironment(cfg *config.Config, envName, providerFlag, clusterName string) (*config.ResolvedEnvironmentConfig, error) {
	if err := cfg.ResolveEnvironments(); err != nil {
		if envName != "" {
			return nil, fmt.Errorf("resolving environment %s: %w", envName, err)
		}
		return nil, nil
	}
	if envName != "" {
		env, ok := cfg.ResolvedEnvironments[envName]
		if !ok {
			names := make([]string, 0, len(cfg.ResolvedEnvironments))
			for n := range cfg.ResolvedEnvironments {
				names = append(names, n)
			}
			sort.Strings(names)
			return nil, fmt.Errorf("environment %q is not configured (have: %s)", envName, strings.Join(names, ", "))
		}
		if providerFlag != "" && env.ResolvedProvider != providerFlag {
			return nil, fmt.Errorf("environment %s is on provider %s, not %s", envName, env.ResolvedProvider, providerFlag)
		}
		return env, nil
	}
	var match *config.ResolvedEnvironmentConfig
	for _, env := range cfg.ResolvedEnvironments {
		if providerFlag != "" && env.ResolvedProvider != providerFlag {
			continue
		}
		for _, n := range helpers.EnvironmentClusterNames("", env) {
			if n == clusterName {
				if match != nil {
					return nil, nil // ambiguous: --env decides
				}
				match = env
				break
			}
		}
	}
	return match, nil
}
