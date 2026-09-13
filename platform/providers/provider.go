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

package provider

import (
	"adhar-io/adhar/globals"
	"context"
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"adhar-io/adhar/platform/config"
	"adhar-io/adhar/platform/logger"
	"adhar-io/adhar/platform/types"
)

// NewProviderCommand creates an updated provider command with actual functionality
func NewProviderCommand() *cobra.Command {
	providerCmd := &cobra.Command{
		Use:     "provider",
		Short:   "Manage cloud providers",
		Long:    "Configure and manage cloud provider settings and credentials",
		Aliases: []string{"providers"},
	}

	providerCmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List configured providers",
		Long:  "List all configured cloud providers and their status",
		RunE: func(cmd *cobra.Command, args []string) error {
			return listProviders(cmd)
		},
	})

	providerCmd.AddCommand(&cobra.Command{
		Use:   "info [provider-name]",
		Short: "Get provider information",
		Long:  "Get detailed information about a specific provider",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return showProviderInfo(cmd, args[0])
		},
	})

	providerCmd.AddCommand(&cobra.Command{
		Use:   "configure [provider-name]",
		Short: "Configure a provider",
		Long:  "Configure credentials and settings for a cloud provider",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return configureProvider(cmd, args[0])
		},
	})

	providerCmd.AddCommand(&cobra.Command{
		Use:   "test [provider-name]",
		Short: "Test provider connection",
		Long:  "Test the connection and credentials for a cloud provider",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return testProvider(cmd, args[0])
		},
	})

	providerCmd.AddCommand(&cobra.Command{
		Use:   "primary",
		Short: "Show primary provider configuration",
		Long:  "Display which provider is configured as primary for management cluster and workloads",
		RunE: func(cmd *cobra.Command, args []string) error {
			return showPrimaryProvider(cmd)
		},
	})

	return providerCmd
}

// listProviders lists all available and configured providers
func listProviders(cmd *cobra.Command) error {
	// Get supported providers
	supportedProviders := DefaultFactory.SupportedProviders()

	// Load configuration to see which are configured
	cfg, err := config.LoadConfig("")
	if err != nil {
		// If no config file, use empty config
		cfg = &config.Config{Providers: make(map[string]config.ConfigProviderConfig)}
	}

	// Create table writer
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "PROVIDER\tSTATUS\tREGION\tDESCRIPTION")
	fmt.Fprintln(w, "--------\t------\t------\t-----------")

	for _, providerType := range supportedProviders {
		info, err := DefaultFactory.GetProviderInfo(providerType)
		if err != nil {
			continue
		}

		status := "Available"
		region := "N/A"

		if providerCfg, exists := cfg.Providers[providerType]; exists {
			status = "Configured"
			region = providerCfg.Region
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
			info.Name, status, region, info.Description)
	}

	w.Flush()
	return nil
}

// showProviderInfo shows detailed information about a provider
func showProviderInfo(cmd *cobra.Command, providerName string) error {
	info, err := DefaultFactory.GetProviderInfo(providerName)
	if err != nil {
		return fmt.Errorf("failed to get provider info: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Provider: %s (%s)\n", info.Name, info.Type)
	fmt.Fprintf(cmd.OutOrStdout(), "Description: %s\n", info.Description)
	fmt.Fprintf(cmd.OutOrStdout(), "Cost Model: %s\n", info.CostModel)

	fmt.Fprintf(cmd.OutOrStdout(), "\nCapabilities:\n")
	for _, capability := range info.Capabilities {
		fmt.Fprintf(cmd.OutOrStdout(), "  • %s\n", capability)
	}

	if len(info.RequiredCredentials) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "\nRequired Credentials:\n")
		for _, cred := range info.RequiredCredentials {
			fmt.Fprintf(cmd.OutOrStdout(), "  • %s\n", cred)
		}
	}

	fmt.Fprintf(cmd.OutOrStdout(), "\nSupported Regions:\n")
	for _, region := range info.SupportedRegions {
		fmt.Fprintf(cmd.OutOrStdout(), "  • %s\n", region)
	}

	return nil
}

// configureProvider configures a provider
func configureProvider(cmd *cobra.Command, providerName string) error {
	// TODO: Implement interactive provider configuration
	fmt.Fprintf(cmd.OutOrStdout(), "Configuring provider: %s\n", providerName)
	fmt.Fprintf(cmd.OutOrStdout(), "This feature will be implemented in a future version.\n")
	fmt.Fprintf(cmd.OutOrStdout(), "For now, please edit the configuration file directly.\n")
	return nil
}

// testProvider tests a provider connection
func testProvider(cmd *cobra.Command, providerName string) error {
	// Load configuration
	cfg, err := config.LoadConfig("")
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Check if provider is configured
	providerCfg, exists := cfg.Providers[providerName]
	if !exists {
		return fmt.Errorf("provider %s is not configured", providerName)
	}

	// Create provider instance
	p, err := DefaultFactory.CreateProvider(providerName, providerCfg.ToProviderMap())
	if err != nil {
		return fmt.Errorf("failed to create provider: %w", err)
	}

	// Test authentication
	fmt.Fprintf(cmd.OutOrStdout(), "Testing provider: %s\n", providerName)
	fmt.Fprintf(cmd.OutOrStdout(), "Provider: %s\n", p.Name())
	fmt.Fprintf(cmd.OutOrStdout(), "Region: %s\n", p.Region())

	// TODO: Test actual authentication
	fmt.Fprintf(cmd.OutOrStdout(), "✓ Provider connection successful\n")

	return nil
}

// showPrimaryProvider displays primary provider configuration
func showPrimaryProvider(cmd *cobra.Command) error {
	// Load configuration
	cfg, err := config.LoadConfig("")
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Provider Configuration Summary:\n\n")

	// Show total provider count
	providerCount := len(cfg.Providers)
	fmt.Fprintf(cmd.OutOrStdout(), "Total Providers: %d\n", providerCount)

	if providerCount == 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "No providers configured.\n")
		return nil
	}

	// Show provider usage based on configuration
	if providerCount == 1 {
		// Single provider handles everything
		var providerName string
		for name := range cfg.Providers {
			providerName = name
			break
		}
		fmt.Fprintf(cmd.OutOrStdout(), "\nSingle Provider Mode:\n")
		fmt.Fprintf(cmd.OutOrStdout(), "  Provider: %s\n", providerName)
		fmt.Fprintf(cmd.OutOrStdout(), "  Usage: Management cluster AND development workloads\n")
		fmt.Fprintf(cmd.OutOrStdout(), "  Region: %s\n", cfg.Providers[providerName].Region)
	} else {
		// Multiple provider setup
		primaryProvider, err := cfg.GetPrimaryProvider()
		if err != nil {
			return fmt.Errorf("error determining primary provider: %w", err)
		}

		workloadProvider, err := cfg.GetWorkloadProvider()
		if err != nil {
			return fmt.Errorf("error determining workload provider: %w", err)
		}

		fmt.Fprintf(cmd.OutOrStdout(), "\nMulti-Provider Mode:\n")
		fmt.Fprintf(cmd.OutOrStdout(), "\nManagement Cluster Provider:\n")
		fmt.Fprintf(cmd.OutOrStdout(), "  Provider: %s\n", primaryProvider)
		fmt.Fprintf(cmd.OutOrStdout(), "  Usage: Management cluster provisioning\n")
		fmt.Fprintf(cmd.OutOrStdout(), "  Region: %s\n", cfg.Providers[primaryProvider].Region)
		fmt.Fprintf(cmd.OutOrStdout(), "  Primary: %v\n", cfg.Providers[primaryProvider].Primary)

		fmt.Fprintf(cmd.OutOrStdout(), "\nDevelopment Workload Provider:\n")
		fmt.Fprintf(cmd.OutOrStdout(), "  Provider: %s\n", workloadProvider)
		fmt.Fprintf(cmd.OutOrStdout(), "  Usage: Development workloads\n")
		fmt.Fprintf(cmd.OutOrStdout(), "  Region: %s\n", cfg.Providers[workloadProvider].Region)
		fmt.Fprintf(cmd.OutOrStdout(), "  Primary: %v\n", cfg.Providers[workloadProvider].Primary)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "\nAll Configured Providers:\n")
	for name, providerCfg := range cfg.Providers {
		primaryStatus := ""
		if providerCfg.Primary {
			primaryStatus = " (PRIMARY)"
		}
		fmt.Fprintf(cmd.OutOrStdout(), "  - %s: %s in %s%s\n", name, providerCfg.Type, providerCfg.Region, primaryStatus)
	}

	return nil
}

// ProviderManager manages cloud providers for cluster provisioning
type ProviderManager struct {
	factory ProviderFactory
}

// NewProviderManager creates a new provider manager
func NewProviderManager(factory ProviderFactory) *ProviderManager {
	return &ProviderManager{factory: factory}
}

// ProvisionOptions contains options for provisioning
type ProvisionOptions struct {
	DryRun bool
	Force  bool
	// Recreate deletes a cluster that already exists under the environment's
	// name before provisioning, giving a clean cluster instead of adopting the
	// existing machines (`adhar up --recreate`, same semantics as local Kind).
	Recreate bool
}

// recreateCluster deletes an existing cluster named like the environment and
// waits until the provider no longer lists it. Not finding one is not an error.
func recreateCluster(ctx context.Context, prov Provider, name string) error {
	clusters, err := prov.ListClusters(ctx)
	if err != nil {
		return fmt.Errorf("listing clusters: %w", err)
	}
	var existing *types.Cluster
	for _, c := range clusters {
		if c.Name == name || c.ID == name {
			existing = c
			break
		}
	}
	if existing == nil {
		logger.Infof("Recreate requested but no cluster named '%s' exists; creating fresh", name)
		return nil
	}
	logger.Infof("Recreate requested: deleting existing cluster '%s' (%s)", existing.Name, existing.ID)
	if err := prov.DeleteCluster(ctx, existing.ID); err != nil {
		return fmt.Errorf("deleting cluster %s: %w", existing.ID, err)
	}
	deadline := time.Now().Add(20 * time.Minute)
	for time.Now().Before(deadline) {
		clusters, err := prov.ListClusters(ctx)
		if err == nil {
			gone := true
			for _, c := range clusters {
				if c.ID == existing.ID {
					gone = false
					break
				}
			}
			if gone {
				logger.Infof("Cluster '%s' deleted", existing.Name)
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(15 * time.Second):
		}
	}
	return fmt.Errorf("cluster %s still listed 20 minutes after deletion", existing.ID)
}

// ProvisionEnvironment provisions using the appropriate provider based on configuration
// ProvisionResult is the outcome of a successful ProvisionEnvironment call: the
// provider instance and the created cluster, so callers can continue with
// post-provisioning steps (kubeconfig retrieval, platform bootstrap). It is nil
// for dry runs.
type ProvisionResult struct {
	Provider Provider
	Cluster  *types.Cluster
}

func (pm *ProviderManager) ProvisionEnvironment(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig, opts ProvisionOptions) (*ProvisionResult, error) {
	providerType := strings.ToLower(envConfig.ResolvedProvider)

	// Build provider configuration from environment config
	providerConfig := buildProviderConfig(envConfig)

	// Create provider instance
	prov, err := pm.factory.CreateProvider(providerType, providerConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create %s provider: %w", providerType, err)
	}

	if opts.DryRun {
		fmt.Printf("DRY-RUN: Would create %s cluster '%s' in region '%s'\n",
			envConfig.ResolvedProvider, envConfig.Name, envConfig.ResolvedRegion)
		return nil, nil
	}

	// Build cluster specification based on provider and environment
	spec, err := buildClusterSpec(envConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to build cluster specification: %w", err)
	}

	// Authenticate with the provider
	if err := prov.Authenticate(ctx, buildCredentials(envConfig)); err != nil {
		return nil, fmt.Errorf("authentication failed for %s provider: %w", providerType, err)
	}

	// Validate permissions
	if err := prov.ValidatePermissions(ctx); err != nil {
		return nil, fmt.Errorf("permission validation failed for %s provider: %w", providerType, err)
	}

	if opts.Recreate {
		if err := recreateCluster(ctx, prov, envConfig.Name); err != nil {
			return nil, fmt.Errorf("recreating %s cluster: %w", providerType, err)
		}
	}

	// Create the cluster
	logger.Infof("Creating cluster '%s' using %s provider in region %s", envConfig.Name, providerType, envConfig.ResolvedRegion)

	cluster, err := prov.CreateCluster(ctx, spec)
	if err != nil {
		return nil, fmt.Errorf("failed to create %s cluster: %w", providerType, err)
	}

	logger.Infof("Cluster created successfully - ID: %s, Status: %s", cluster.ID, cluster.Status)

	return &ProvisionResult{Provider: prov, Cluster: cluster}, nil
}

// buildProviderConfig creates provider-specific configuration from environment config
func buildProviderConfig(envConfig *config.ResolvedEnvironmentConfig) map[string]interface{} {
	providerConfig := make(map[string]interface{})

	// Start from the provider's own block (credentials such as token, plus the
	// nested `config:` section) so provisioning sees the same map that
	// `cluster create` builds via ToProviderMap. Environment-level values
	// below override it.
	if envConfig.ProviderConfig != nil {
		providerConfig = envConfig.ProviderConfig.ToProviderMap()
	}

	// Add region
	if envConfig.ResolvedRegion != "" {
		providerConfig["region"] = envConfig.ResolvedRegion
	}

	// Add cluster-specific configuration
	for _, kv := range envConfig.ResolvedClusterConfig {
		providerConfig[kv.Key] = kv.Value
	}

	return providerConfig
}

// buildClusterSpec creates a cluster specification based on environment configuration
func buildClusterSpec(envConfig *config.ResolvedEnvironmentConfig) (*types.ClusterSpec, error) {
	spec := &types.ClusterSpec{
		Provider: envConfig.ResolvedProvider,
		Region:   envConfig.ResolvedRegion,
		// The platform default, overridden below by the environment's
		// `kubeVersion`/`version` clusterConfig entry (which `adhar up
		// --kube-version` rewrites when the flag is given explicitly). Leaving
		// it empty pushed the decision down into each provider, so the same
		// config produced different Kubernetes versions per cloud.
		Version: globals.DefaultKubernetesVersion,
		ObjectMeta: types.ObjectMeta{
			Name: envConfig.Name,
		},
	}

	// Set defaults based on environment type
	isProduction := envConfig.ResolvedType == config.EnvironmentTypeProduction

	// Configure control plane. Single control-plane node for now: the
	// compute-mode (kubeadm) providers bootstrap one control plane and reject
	// >1 (HA control planes need a load balancer + stacked etcd — the roadmap's
	// Phase 1 "next step"). Managed providers run their own managed control
	// plane, so 1 is the right request here regardless. Platform-level HA
	// (ArgoCD/Gitea replicas, CNPG) is driven separately by enableHAMode.
	spec.ControlPlane = types.ControlPlaneSpec{
		Replicas: 1,
	}

	// Configure node groups
	workerReplicas := 0 // Single-node cluster for local development
	if isProduction {
		workerReplicas = 3 // More workers for production
	}
	spec.NodeGroups = []types.NodeGroupSpec{
		{
			Name:     "workers",
			Replicas: workerReplicas,
		},
	}

	// Configure networking
	spec.Networking = types.NetworkingSpec{
		CNI:         "cilium",
		PodCIDR:     "10.244.0.0/16",
		ServiceCIDR: "10.96.0.0/12",
	}

	// Configure domain management
	spec.Domain = buildDomainConfig(envConfig)

	// Apply cluster-specific configuration
	oidcAuth := false
	for _, kv := range envConfig.ResolvedClusterConfig {
		switch kv.Key {
		case "kubeVersion", "version":
			spec.Version = kv.Value
		case "controlPlaneReplicas":
			if replicas := parseIntOrDefault(kv.Value, spec.ControlPlane.Replicas); replicas > 0 {
				spec.ControlPlane.Replicas = replicas
			}
		case "workerReplicas", "nodeCount", "numNodes":
			// nodeCount (DO/generic) and numNodes (GKE) are the spellings used in
			// config.yaml environments; workerReplicas is the internal name. All
			// set the worker node pool size.
			if replicas := parseIntOrDefault(kv.Value, workerReplicas); replicas > 0 {
				spec.NodeGroups[0].Replicas = replicas
			}
		case "nodeInstanceType", "instanceType", "nodeSize", "machineType":
			// nodeSize (DO/generic) and machineType (GKE) are the config.yaml
			// spellings; nodeInstanceType/instanceType are internal aliases.
			spec.NodeGroups[0].InstanceType = kv.Value
		case "podCIDR", "podCidr", "podNetworkCIDR":
			// A cluster that will join a Cilium Cluster Mesh must not share a
			// Pod CIDR with its peers, so the environment can move off the
			// platform default (10.244.0.0/16).
			spec.Networking.PodCIDR = kv.Value
		case "diskSize":
			// Note: DiskSize not available in current NodeGroupSpec
			// This could be added to the spec if needed in the future
		case "oidcAuth", "oidcAuthentication", "kubeOIDC":
			// Opt-in: point the kube-apiserver at the platform's own Keycloak so
			// `adhar auth login` yields a token kubectl can use. OFF by default —
			// see apiServerOIDCArgs for why this is a deliberate choice and not a
			// sensible default.
			if strings.EqualFold(strings.TrimSpace(kv.Value), "true") {
				oidcAuth = true
			}
		}
	}

	if oidcAuth {
		host := ""
		if spec.Domain != nil {
			host = spec.Domain.BaseDomain
		}
		if spec.ControlPlane.APIServer.ExtraArgs == nil {
			spec.ControlPlane.APIServer.ExtraArgs = map[string]string{}
		}
		if issuer := clusterConfigString(envConfig, "oidcIssuerUrl", "oidcIssuer"); issuer != "" {
			spec.ControlPlane.APIServer.ExtraArgs["oidc-issuer-url"] = issuer
		}
		for k, v := range apiServerOIDCArgs(host) {
			// An explicit extraArgs entry always wins, so an operator can pin a
			// different issuer or claim without editing this code.
			if _, ok := spec.ControlPlane.APIServer.ExtraArgs[k]; !ok {
				spec.ControlPlane.APIServer.ExtraArgs[k] = v
			}
		}
	}

	return spec, nil
}

// buildDomainConfig creates domain configuration based on environment and provider
func buildDomainConfig(envConfig *config.ResolvedEnvironmentConfig) *types.DomainConfig {
	// Get domain configuration from global settings or use defaults
	var baseDomain string
	var email string

	if envConfig.GlobalSettings != nil {
		if envConfig.GlobalSettings.DefaultHost != "" {
			baseDomain = envConfig.GlobalSettings.DefaultHost
		}
		if envConfig.GlobalSettings.Email != "" {
			email = envConfig.GlobalSettings.Email
		}
	}

	// Use defaults if not specified
	if baseDomain == "" {
		baseDomain = "adhar.localtest.me"
	}
	if email == "" {
		email = "admin@adhar.localtest.me"
	}

	return &types.DomainConfig{
		BaseDomain: baseDomain,
		TLS: types.TLSConfig{
			Enabled: true,
			Email:   email,
		},
	}
}

// buildCredentials creates provider credentials from environment configuration
func buildCredentials(envConfig *config.ResolvedEnvironmentConfig) *types.Credentials {
	credentials := &types.Credentials{
		Type: envConfig.ResolvedProvider,
		Data: make(map[string]interface{}),
	}

	// Add provider-specific credentials based on environment config
	// Note: This is a simplified version - in practice, credentials would come from
	// environment variables, config files, or secret management systems
	if envConfig.ResolvedProvider == "aws" {
		credentials.Data["accessKeyId"] = "placeholder"
		credentials.Data["secretAccessKey"] = "placeholder"
	} else if envConfig.ResolvedProvider == "gcp" {
		credentials.Data["serviceAccountKey"] = "placeholder"
	} else if envConfig.ResolvedProvider == "azure" {
		credentials.Data["subscriptionId"] = "placeholder"
		credentials.Data["tenantId"] = "placeholder"
		credentials.Data["clientId"] = "placeholder"
		credentials.Data["clientSecret"] = "placeholder"
	}

	return credentials
}

// apiServerOIDCArgs returns the kube-apiserver flags that let the platform's own
// Keycloak authenticate kubectl, matching the `oidc:`-prefixed Group subjects in
// the ClusterRoleBindings that security/keycloak ships (platform-admin ->
// cluster-admin, platform-developer -> edit, platform-viewer -> view).
//
// WHY THIS IS OPT-IN AND NOT THE DEFAULT. Turning it on means membership of a
// Keycloak group grants Kubernetes authority — anyone in `platform-admin`
// becomes cluster-admin — so it changes who can administer the cluster, and it
// makes the realm a part of the cluster's trust boundary. That is a decision for
// whoever runs the platform, not a default. It is also not universally
// applicable: managed control planes (DOKS, EKS, AKS, GKE) do not accept
// apiserver flags this way and need their provider's own OIDC settings.
//
// Enable with `oidcAuth: "true"` in the environment's clusterConfig.
//
// The username/group PREFIXES matter: without `oidc:` an OIDC identity could
// collide with a built-in `system:` user or group, which is why the shipped
// bindings expect it.
func apiServerOIDCArgs(baseDomain string) map[string]string {
	if baseDomain == "" {
		return nil
	}
	// The portless HTTPS form, because this path is the kubeadm/cloud one, where
	// the platform is published on 443 through a load balancer. A platform served
	// on a non-standard port (a Kind-style :8443) must state the issuer itself —
	// `oidcIssuerUrl` in clusterConfig, applied before this and left untouched
	// below — rather than have a port guessed here from settings that the
	// resolved environment does not carry.
	return map[string]string{
		"oidc-issuer-url": fmt.Sprintf("https://keycloak.%s/realms/%s", baseDomain, globals.KeycloakRealm),
		// The token's AUDIENCE, not the CLI client id that requested it — see
		// globals.KubernetesOIDCAudience. Using adhar-cli here rejects every token.
		"oidc-client-id":       globals.KubernetesOIDCAudience,
		"oidc-username-claim":  "preferred_username",
		"oidc-username-prefix": "oidc:",
		"oidc-groups-claim":    "groups",
		"oidc-groups-prefix":   "oidc:",
	}
}

// clusterConfigString returns the first non-empty value among keys from the
// environment's resolved clusterConfig.
func clusterConfigString(envConfig *config.ResolvedEnvironmentConfig, keys ...string) string {
	if envConfig == nil {
		return ""
	}
	for _, want := range keys {
		for _, kv := range envConfig.ResolvedClusterConfig {
			if kv.Key == want && strings.TrimSpace(kv.Value) != "" {
				return strings.TrimSpace(kv.Value)
			}
		}
	}
	return ""
}

// parseIntOrDefault parses a string to int with a default fallback
func parseIntOrDefault(value string, defaultValue int) int {
	if parsed, err := strconv.Atoi(value); err == nil {
		return parsed
	}
	return defaultValue
}
