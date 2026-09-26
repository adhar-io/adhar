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
	"strings"

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

// OrphanVolumeSweeper is implemented by providers that can remove unattached,
// PersistentVolume-shaped storage WITHOUT a live cluster to hang it off.
//
// It is an OPTIONAL capability rather than part of the Provider interface: only
// the providers whose CSI driver leaves disks behind need it, and adding a method
// to the interface would force six other providers to grow a stub.
//
// It exists because `--purge-orphaned-volumes` used to be reachable only from
// inside cluster deletion. That made the warning a teardown prints —
// "they are kept, remove them with adhar down ... --purge-orphaned-volumes" —
// impossible to act on: by the time you read it the cluster is gone, so the
// lookup finds nothing and the sweep never runs. 74 GCP disks (771 GB) were left
// billing behind that hint on 2026-09-26.
type OrphanVolumeSweeper interface {
	// PurgeOrphanedVolumes deletes detached pvc-* volumes that no surviving
	// cluster claims, and reports how many went and what refused. One volume
	// failing is not a reason to abandon the rest, so errors are collected
	// rather than returned as a single failure.
	PurgeOrphanedVolumes(ctx context.Context) (deleted int, errs []string)
}

// BuildProvider constructs one configured provider by name, applying the same
// per-run options (`purgeOrphanedVolumes`, …) that FindCluster applies.
//
// FindCluster builds providers internally and throws them away when it does not
// find the cluster, which left a caller that still needs to talk to that
// provider — to sweep leaked volumes, say — with nothing to talk to.
func BuildProvider(cfg *config.Config, providerName string, providerOpts map[string]interface{}) (pfactory.Provider, error) {
	if cfg == nil {
		return nil, fmt.Errorf("no configuration loaded")
	}
	providerCfg, ok := cfg.Providers[providerName]
	if !ok {
		return nil, fmt.Errorf("provider %q is not configured", providerName)
	}
	providerMap := providerCfg.ToProviderMap()
	for k, v := range providerOpts {
		providerMap[k] = v
	}
	return pfactory.DefaultFactory.CreateProvider(providerName, providerMap)
}

// LookupFailure records a provider that could not be consulted at all, and why.
// A provider in this list has told us NOTHING about whether the cluster exists.
type LookupFailure struct {
	Provider string
	Reason   string
}

// NotFoundError says the cluster was not located. It carries WHICH providers
// were actually consulted and which could not be, because those two cases look
// identical from the outside and must not be treated the same way.
//
// This type exists because of a real incident (2026-09-25): `adhar down -f
// config.yaml --env production` printed "✓ Successfully tore down Adhar
// platform! Cloud resources for production have been removed" while five GCE
// instances, 79 disks, a VPC, 11 firewall rules and a load balancer kept running
// and billing. That config file defines only the `kind` provider, so the GCP
// project was never queried — and a bare `error` meant the caller could not tell
// "I looked everywhere and it is gone" from "I never looked".
type NotFoundError struct {
	Name string
	// Searched are providers that answered a list request successfully.
	Searched []string
	// Failures are providers that could not be consulted.
	Failures []LookupFailure
}

func (e *NotFoundError) Error() string {
	msg := fmt.Sprintf("cluster %q not found", e.Name)
	if len(e.Searched) > 0 {
		msg += fmt.Sprintf(" (searched: %s)", strings.Join(e.Searched, ", "))
	} else {
		msg += " (no provider could be searched)"
	}
	for _, f := range e.Failures {
		msg += fmt.Sprintf("; %s could not be consulted: %s", f.Provider, f.Reason)
	}
	return msg
}

// Conclusive reports whether "not found" can be trusted to mean "not there".
// It is false when any provider could not be consulted, because then the
// cluster may well exist somewhere we failed to look.
func (e *NotFoundError) Conclusive() bool { return len(e.Failures) == 0 && len(e.Searched) > 0 }

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
	// Recorded so a caller can tell "searched and absent" from "never asked".
	var searched []string
	var failures []LookupFailure
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
			failures = append(failures, LookupFailure{Provider: providerName, Reason: err.Error()})
			continue
		}
		clusters, err := p.ListClusters(ctx)
		if err != nil {
			warn(fmt.Sprintf("provider %s could not be queried: %v", providerName, err))
			failures = append(failures, LookupFailure{Provider: providerName, Reason: err.Error()})
			continue
		}
		searched = append(searched, providerName)
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
			if clusters, listErr := p.ListClusters(ctx); listErr == nil {
				searched = append(searched, "kind (implicit)")
				for _, c := range clusters {
					if c.Name == name {
						return &ResolvedCluster{Cluster: c, Provider: p, ProviderName: "kind"}, nil
					}
				}
			}
			// A missing local Kind is not a failure to report: it is the normal
			// state on a machine that only ever ran cloud environments.
		}
	}

	return nil, &NotFoundError{Name: name, Searched: searched, Failures: failures}
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
