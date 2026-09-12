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

package helpers

import (
	"context"
	"fmt"
	"sort"

	"adhar-io/adhar/platform/config"
	pfactory "adhar-io/adhar/platform/providers"
	ptypes "adhar-io/adhar/platform/types"
)

// ResolvedCluster is a cluster located in one of the configured providers,
// together with the provider that owns it.
type ResolvedCluster struct {
	Cluster      *ptypes.Cluster
	Provider     pfactory.Provider
	ProviderName string
}

// IsAdharManaged reports whether Adhar created this cluster. A cluster without
// the tag is still deletable, but the caller should say so before acting.
func (r ResolvedCluster) IsAdharManaged() bool {
	if r.Cluster == nil || r.Cluster.Tags == nil {
		return false
	}
	return r.Cluster.Tags["adhar.io/managed-by"] == "adhar"
}

// FindCluster locates a cluster by name across every configured provider, and
// falls back to Kind when the config does not mention it.
//
// This lives here, rather than inside one command, because `adhar down` and
// `adhar cluster delete` must agree about what "the cluster" is. They did not:
// `down` shelled out to `kind delete cluster` unconditionally, so on a cloud
// environment it destroyed nothing and still reported success, leaving the
// droplets, volumes and load balancer running (and billing).
//
// warn receives non-fatal problems -- a provider whose credentials are missing,
// say. They are surfaced rather than swallowed, because "cluster not found"
// and "could not ask the provider" look identical to a user otherwise.
func FindCluster(ctx context.Context, cfg *config.Config, name string, providerOpts map[string]interface{}, warn func(string)) (*ResolvedCluster, error) {
	if cfg == nil {
		return nil, fmt.Errorf("no configuration loaded")
	}
	if name == "" {
		return nil, fmt.Errorf("no cluster name given")
	}
	if warn == nil {
		warn = func(string) {}
	}

	// Deterministic order: the same config must always resolve the same way.
	providerNames := make([]string, 0, len(cfg.Providers))
	for providerName := range cfg.Providers {
		providerNames = append(providerNames, providerName)
	}
	sort.Strings(providerNames)

	kindConfigured := false
	for _, providerName := range providerNames {
		if providerName == "kind" {
			kindConfigured = true
		}
		providerCfg := cfg.Providers[providerName]
		providerMap := providerCfg.ToProviderMap()
		for k, v := range providerOpts {
			providerMap[k] = v
		}

		p, err := pfactory.DefaultFactory.CreateProvider(providerName, providerMap)
		if err != nil {
			warn(fmt.Sprintf("provider %s could not be initialised: %v", providerName, err))
			continue
		}
		clusters, err := p.ListClusters(ctx)
		if err != nil {
			warn(fmt.Sprintf("provider %s could not be queried: %v", providerName, err))
			continue
		}
		for _, c := range clusters {
			if c.Name == name {
				return &ResolvedCluster{Cluster: c, Provider: p, ProviderName: providerName}, nil
			}
		}
	}

	// Kind is the local default and is usually absent from a cloud config file,
	// so look there too before declaring the cluster missing.
	if !kindConfigured {
		p, err := pfactory.DefaultFactory.CreateProvider("kind", map[string]interface{}{
			"kindPath":    "kind",
			"kubectlPath": "kubectl",
		})
		if err == nil {
			if clusters, err := p.ListClusters(ctx); err == nil {
				for _, c := range clusters {
					if c.Name == name {
						return &ResolvedCluster{Cluster: c, Provider: p, ProviderName: "kind"}, nil
					}
				}
			}
		}
	}

	return nil, fmt.Errorf("cluster %q not found in any configured provider", name)
}

// EnvironmentClusterName returns the name under which an environment's cluster
// is registered with its provider.
//
// It is the ENVIRONMENT name. buildClusterSpec (platform/providers/provider.go)
// sets ObjectMeta.Name from envConfig.Name, so that is what every provider
// lists the cluster as.
//
// It is deliberately NOT the `clusterConfig.name` entry. That value names the
// platform for tagging -- `adhar-mgmt` in the shipped DigitalOcean config,
// which appears in droplet tags -- while the cluster itself is still `dev`.
// Teardown that keyed on clusterConfig.name would look for a cluster that does
// not exist, report "nothing to remove", and leave the whole environment
// running.
func EnvironmentClusterName(env *config.ResolvedEnvironmentConfig) string {
	if env == nil {
		return ""
	}
	return env.Name
}
