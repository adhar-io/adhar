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

package up

import (
	"context"
	"fmt"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
)

const (
	recreateClusterUsage           = "Delete cluster first if it already exists"
	devPasswordUsage               = "Set password 'developer' for admin users (argocd & gitea)"
	kubeVersionUsage               = "Kubernetes version for the kind cluster"
	extraPortsMappingUsage         = "Extra ports to expose (e.g., '22:32222,9090:39090')"
	registryConfigUsage            = "Registry config paths (uses first existing one)"
	kindConfigPathUsage            = "Custom kind config file path or URL"
	hostUsage                      = "Host name for cluster resources"
	ingressHostUsage               = "Custom ingress host name (for proxy setups)"
	protocolUsage                  = "Protocol for web UIs (http or https)"
	portUsage                      = "Port for web UIs"
	pathRoutingUsage               = "Use single domain with path routing"
	extraPackagesUsage             = "Paths to custom package locations"
	packageCustomizationFilesUsage = "Package customization files (e.g., argocd:/tmp/argocd.yaml)"
	noExitUsage                    = "Keep running to continuously sync directories"
)

var (
	// Flags
	recreateCluster           bool
	devPassword               bool
	kubeVersion               string
	extraPortsMapping         string
	kindConfigPath            string
	extraPackages             []string
	registryConfig            []string
	packageCustomizationFiles []string
	noExit                    bool
	protocol                  string
	host                      string
	ingressHost               string
	port                      string
	pathRouting               bool
	verbose                   bool // Add verbose flag

	// Production cluster provisioning flags
	configFile  string
	environment string
	dryRun      bool
	force       bool
)

// UpCmd represents the up command
var UpCmd = &cobra.Command{
	Use:     "up",
	Aliases: []string{"create"},
	Short:   "Create an Adhar IDP",
	Long: `The "adhar up" command is used to create and configure an Adhar Internal Developer Platform (IDP).

This command supports two primary use cases:

1. Local Development: Developers can use 'adhar up' to quickly spin up a local Adhar cluster 
   for testing and development purposes. By default, it sets up a Kubernetes cluster using 
   Kind (Kubernetes in Docker) and provisions essential platform components like ArgoCD, 
   Gitea, and Nginx.

   Example:
     adhar up
     # List available environments: adhar get envs -f config.yaml

2. Production Setup: For production environments, 'adhar up' can be used with a 
   configuration file to deploy the Adhar platform on cloud infrastructure. The 
   configuration file allows customization of cluster settings, package configurations, 
   and resource allocations.

   Example:
     adhar up -f config.yaml
     adhar up -f config.yaml --env prod  # Deploy specific environment

Key Features:
• Supports local development with minimal setup
• Configures Kubernetes clusters in your favorite cloud vendor with custom settings
• Provisions core platform components like Cilium, ArgoCD, Gitea, Grafana, Keycloak, Backstage, Nginx and more
• Allows customization of packages and configurations
• Supports local development with rapid iteration
• Brings holistic governance to your development environment
• Enables developers to continuously sync local directories for rapid iteration
• Supports cloud-based production deployments with configuration files

For more information, visit the documentation at https://adhar.io/docs`,
	Example: `  # Create local development cluster
  adhar up

  # Deploy production platform with configuration
  adhar up -f config.yaml

  # Deploy specific environment
  adhar up -f config.yaml --env prod

  # Preview changes without applying
  adhar up --dry-run

  # Customize packages
  adhar up -p /path/to/custom/packages

  # Set custom host and port
  adhar up --host mydomain.com --port 8080`,
	RunE:         create,
	PreRunE:      preCreateE,
	SilenceUsage: true,
}

func init() {
	// cluster related flags
	UpCmd.PersistentFlags().BoolVar(&recreateCluster, "recreate", false, recreateClusterUsage)
	UpCmd.PersistentFlags().BoolVar(&devPassword, "dev-password", false, devPasswordUsage)
	UpCmd.PersistentFlags().StringVar(&kubeVersion, "kube-version", "v1.33.2", kubeVersionUsage)
	UpCmd.PersistentFlags().StringVar(&extraPortsMapping, "extra-ports", "", extraPortsMappingUsage)
	UpCmd.PersistentFlags().StringVar(&kindConfigPath, "kind-config", "", kindConfigPathUsage)
	UpCmd.PersistentFlags().StringSliceVar(&registryConfig, "registry-config", []string{}, registryConfigUsage)
	UpCmd.PersistentFlags().Lookup("registry-config").NoOptDefVal = "$XDG_RUNTIME_DIR/containers/auth.json,$HOME/.docker/config.json"

	// in-cluster resources related flags
	UpCmd.PersistentFlags().StringVar(&host, "host", "adhar.localtest.me", hostUsage)
	UpCmd.PersistentFlags().StringVar(&ingressHost, "ingress-host-name", "", ingressHostUsage)
	UpCmd.PersistentFlags().StringVar(&protocol, "protocol", "https", protocolUsage)
	UpCmd.PersistentFlags().StringVar(&port, "port", "8443", portUsage)
	UpCmd.PersistentFlags().BoolVar(&pathRouting, "use-path-routing", true, pathRoutingUsage)
	UpCmd.Flags().StringSliceVarP(&extraPackages, "package", "p", []string{"platform/stack"}, extraPackagesUsage)
	UpCmd.Flags().StringSliceVarP(&packageCustomizationFiles, "package-custom-file", "e", []string{}, packageCustomizationFilesUsage)

	// adhar related flags
	UpCmd.Flags().BoolVarP(&noExit, "watch", "w", true, noExitUsage)
	UpCmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Enable debug logging") // Add verbose flag

	// Production cluster provisioning flags
	UpCmd.Flags().StringVarP(&configFile, "file", "f", "", "Configuration file for production cluster")
	UpCmd.Flags().StringVar(&environment, "env", "", "Target environment (dev, test, prod)")
	UpCmd.Flags().BoolVarP(&dryRun, "dry-run", "d", false, "Preview changes without applying")
	UpCmd.Flags().BoolVarP(&force, "force", "F", false, "Force operation, ignoring warnings")
}

func preCreateE(cmd *cobra.Command, args []string) error {
	// Set log level based on verbose flag or global debug flag
	debugFlag, _ := cmd.Root().PersistentFlags().GetBool("debug")
	if verbose || debugFlag {
		logger.CLILogLevel = "debug"
		_ = logger.SetLogLevel("debug")
	} else {
		logger.CLILogLevel = "info"
		_ = logger.SetLogLevel("info")
	}

	// Set colored output (enable by default, disable if NO_COLOR is set)
	logger.CLIColoredOutput = cmd.Root().PersistentFlags().Lookup("no-color") == nil || !cmd.Root().PersistentFlags().Lookup("no-color").Changed

	return logger.SetupKubernetesLogging()
}

func create(cmd *cobra.Command, args []string) error {
	ctx, ctxCancel := context.WithCancel(cmd.Context())
	defer ctxCancel()

	// Check if this is a production setup (config file provided)
	if configFile != "" {
		fmt.Printf("🏭 %s\n", helpers.BoldStyle.Render("Production Platform Provisioning Mode"))
		fmt.Printf("Configuration file: %s\n", configFile)
		if environment != "" {
			fmt.Printf("Target environment: %s\n", environment)
		} else {
			fmt.Printf("Mode: Complete platform provisioning (all environments)\n")
		}
		fmt.Println()
		return createProductionCluster(ctx, cmd, args)
	}

	// Local development mode
	fmt.Printf("🏠 %s\n", helpers.BoldStyle.Render("Local Development Mode"))
	fmt.Printf("Creating Kind-based Kubernetes cluster with essential platform components\n")

	// Perform pre-flight checks
	if err := performLocalPreflightChecks(); err != nil {
		return fmt.Errorf("pre-flight checks failed: %w", err)
	}

	fmt.Println()

	// Create local development cluster using new ProviderManager
	return createLocalDevelopmentCluster(ctx, cmd, args)
}
