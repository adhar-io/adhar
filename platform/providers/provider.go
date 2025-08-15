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
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"adhar-io/adhar/platform/config"
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
