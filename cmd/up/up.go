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

// Package up implements the `adhar up` command, which provisions a complete
// Adhar Internal Developer Platform.
//
// It supports two modes selected by the presence of the --file flag:
//
//   - Local development mode (default): creates a local Kind cluster and runs
//     the platform controllers in-process (see local.go).
//   - Production mode (-f config.yaml): provisions a cloud cluster from a
//     resolved configuration file using the provider factory (see production.go).
//
// Shared helpers used by both modes live in common.go.
package up

import (
	"context"
	"fmt"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
)

const (
	recreateClusterUsage           = "🗑️ Delete existing cluster before creating new one"
	devPasswordUsage               = "🔑 Set password 'developer' for admin users (ArgoCD & Gitea)"
	kubeVersionUsage               = "🐳 Kubernetes version for Kind cluster (e.g., v1.36.1)"
	extraPortsMappingUsage         = "🔌 Extra ports to expose (e.g., '22:32222,9090:39090')"
	registryConfigUsage            = "📦 Container registry config paths (uses first existing one)"
	kindConfigPathUsage            = "⚙️ Custom Kind configuration file path or URL"
	hostUsage                      = "🌐 Host name for cluster resources (default: adhar.localtest.me)"
	ingressHostUsage               = "🚪 Custom ingress host name for proxy setups"
	protocolUsage                  = "🔒 Protocol for web UIs (http or https)"
	portUsage                      = "🚪 Port for web UIs (default: 8443)"
	pathRoutingUsage               = "🛣️ Use single domain with path routing"
	extraPackagesUsage             = "📦 Paths to custom package locations"
	packageCustomizationFilesUsage = "⚙️ Package customization files (e.g., argocd:/tmp/argocd.yaml)"
	noExitUsage                    = "🔄 Keep running to continuously sync directories"
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
	verbose                   bool

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
	Short:   "🚀 Launch Adhar Internal Developer Platform",
	Long: `🚀 **Adhar Internal Developer Platform Launcher**

The "adhar up" command spins up a complete internal developer platform using industry 
standard technologies like Kubernetes, ArgoCD, Gitea, and Crossplane with only Docker 
required as a dependency.

This can be useful in several ways:

• **Local Development**: Platform engineers can use 'adhar up' to quickly spin up a 
  local Adhar cluster for testing and development purposes
• **Reference Implementation**: Create a single binary which demonstrates a complete 
  IDP reference implementation
• **CI Integration**: Use within CI to perform integration testing
• **Demo Environment**: Perfect for demonstrating platform capabilities to stakeholders

**Local Development Mode (Default):**
  adhar up
  # Creates Kind cluster with ArgoCD, Gitea, Crossplane, and 48+ platform tools

**Production Setup:**
  adhar up -f config.yaml
  # Creates production cluster with complete platform stack on your preferred Cloud provider

**Key Features:**
• 🐳 Docker-only dependency (no external tools required)
• 🚀 Single command to spin up complete platform
• 🔧 Industry standard stack (Kubernetes, ArgoCD, Gitea, Crossplane)
• 📦 60+ integrated platform tools and services
• 🌐 Multi-cloud provider support (Kind, DigitalOcean, GCP, AWS, Azure, Civo)
• 🔒 Security by default with zero-trust networking
• 📊 GitOps-driven operations with ArgoCD
• 🎯 Perfect for platform engineers and DevOps teams

For more information, visit: https://github.com/adhar-io/adhar`,
	Example: `  # 🚀 Quick Start - Local Development
  adhar up
  # Spins up complete platform with Kind cluster

  # 🏭 Production Deployment
  adhar up -f config.yaml

  # 👀 Preview Mode
  adhar up --dry-run

  # 🔄 Development Mode
  adhar up --watch --verbose`,
	RunE:         create,
	PreRunE:      preCreateE,
	SilenceUsage: true,
}

func init() {
	// cluster related flags
	UpCmd.PersistentFlags().BoolVar(&recreateCluster, "recreate", false, recreateClusterUsage)
	UpCmd.PersistentFlags().BoolVar(&devPassword, "dev-password", false, devPasswordUsage)
	UpCmd.PersistentFlags().StringVar(&kubeVersion, "kube-version", globals.DefaultKubernetesVersion, kubeVersionUsage)
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
	UpCmd.Flags().Bool("no-exit", false, "Keep running after initial sync (don't exit)")

	// adhar related flags
	UpCmd.Flags().BoolVarP(&noExit, "watch", "w", true, noExitUsage)
	UpCmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Enable debug logging") // Add verbose flag

	// Production cluster provisioning flags
	UpCmd.Flags().StringVarP(&configFile, "file", "f", "", "Configuration file for production cluster")
	UpCmd.Flags().StringVar(&environment, "env", "", "Target environment (dev, test, prod)")
	UpCmd.Flags().BoolVarP(&dryRun, "dry-run", "d", false, "Preview changes without applying")
	UpCmd.Flags().BoolVarP(&force, "force", "F", false, "Force operation, ignoring warnings")
}

// preCreateE sets the log level based on the verbose flag or global debug flag
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

// create is the RunE entry point for UpCmd. It prints a mode-specific banner and
// dispatches to production provisioning when --file is set, otherwise to local
// development cluster creation.
func create(cmd *cobra.Command, args []string) error {
	// Create a new context with cancel function to support graceful shutdown
	ctx, ctxCancel := context.WithCancel(cmd.Context())
	defer ctxCancel()

	// The banner is already printed once by the root command's PersistentPreRun
	// (printHeader) — do not repeat it here. Production mode shows a compact
	// config summary; local mode goes straight to the live stage tracker.
	if configFile != "" {
		fmt.Printf("  %s   %s\n", helpers.MutedStyle.Render("mode  "), helpers.InfoStyle.Render("production"))
		fmt.Printf("  %s   %s\n", helpers.MutedStyle.Render("config"), helpers.InfoStyle.Render(configFile))
		if environment != "" {
			fmt.Printf("  %s   %s\n", helpers.MutedStyle.Render("env   "), helpers.InfoStyle.Render(environment))
		} else {
			fmt.Printf("  %s   %s\n", helpers.MutedStyle.Render("env   "), helpers.InfoStyle.Render("all environments"))
		}
		fmt.Println()
		return createProductionCluster(ctx, cmd, args, ctxCancel)
	}

	// Local development mode — the stage tracker renders all provisioning output.
	return createLocalDevelopmentCluster(ctx, cmd, args, ctxCancel)
}
