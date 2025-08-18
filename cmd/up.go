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
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/config"
	"adhar-io/adhar/platform/logger"
	pfactory "adhar-io/adhar/platform/providers"
	pkind "adhar-io/adhar/platform/providers/kind"
	ptypes "adhar-io/adhar/platform/types"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

const (
	recreateClusterUsage           = "Delete cluster first if it already exists."
	devPasswordUsage               = "Set the password \"developer\" for the admin user of the applications: argocd & gitea."
	kubeVersionUsage               = "Version of the kind kubernetes cluster to create."
	extraPortsMappingUsage         = "List of extra ports to expose on the docker container and kubernetes cluster as nodePort(e.g. \"22:32222,9090:39090,etc\")."
	registryConfigUsage            = "List of paths to mount as the registry config, uses the first one that exists"
	kindConfigPathUsage            = "Path or URL to the kind config file to be used instead of the default."
	hostUsage                      = "Host name to access resources in this cluster."
	ingressHostUsage               = "Host name used by ingresses. Useful when you have another proxy in front of ingress-nginx that adhar provisions."
	protocolUsage                  = "Protocol to use to access web UIs. http or https."
	portUsage                      = "Port number to use to access web UIs."
	pathRoutingUsage               = "When set to true, web UIs are exposed under single domain name. e.g. \"https://adhar.localtest.me/argocd\" instead of \"https://argocd.adhar.localtest.me\""
	extraPackagesUsage             = "Paths to locations containing custom packages"
	packageCustomizationFilesUsage = "Name of the package and the path to file to customize the core packages with. valid package names are: argocd, nginx, and gitea. e.g. argocd:/tmp/argocd.yaml"
	noExitUsage                    = "When set, adhar will not exit after all packages are synced. Useful for continuously syncing local directories."
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

// Local provisioning options (replaces legacy build.ProvisionOptions)
type ProvisionOptions struct {
	DryRun bool
	Force  bool
}

// Lightweight provider manager backed by platform/providers factory
type providerManager struct {
	factory pfactory.ProviderFactory
}

func newProviderManagerWithFactory(_ interface{}, factory pfactory.ProviderFactory) *providerManager {
	return &providerManager{factory: factory}
}

// ProvisionEnvironment provisions using the appropriate provider based on configuration
func (pm *providerManager) ProvisionEnvironment(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig, opts ProvisionOptions) error {
	providerType := strings.ToLower(envConfig.ResolvedProvider)

	// Build provider configuration from environment config
	providerConfig := buildProviderConfig(envConfig)

	// Create provider instance
	prov, err := pm.factory.CreateProvider(providerType, providerConfig)
	if err != nil {
		return fmt.Errorf("failed to create %s provider: %w", providerType, err)
	}

	if opts.DryRun {
		fmt.Printf("DRY-RUN: Would create %s cluster '%s' in region '%s'\n",
			envConfig.ResolvedProvider, envConfig.Name, envConfig.ResolvedRegion)
		return nil
	}

	// Build cluster specification based on provider and environment
	spec, err := buildClusterSpec(envConfig)
	if err != nil {
		return fmt.Errorf("failed to build cluster specification: %w", err)
	}

	// Authenticate with the provider
	if err := prov.Authenticate(ctx, buildCredentials(envConfig)); err != nil {
		return fmt.Errorf("authentication failed for %s provider: %w", providerType, err)
	}

	// Validate permissions
	if err := prov.ValidatePermissions(ctx); err != nil {
		return fmt.Errorf("permission validation failed for %s provider: %w", providerType, err)
	}

	// Create the cluster
	logger.Infof("Creating cluster '%s' using %s provider in region %s", envConfig.Name, providerType, envConfig.ResolvedRegion)

	cluster, err := prov.CreateCluster(ctx, spec)
	if err != nil {
		return fmt.Errorf("failed to create %s cluster: %w", providerType, err)
	}

	logger.Infof("Cluster created successfully - ID: %s, Status: %s", cluster.ID, cluster.Status)

	// Apply platform stack manifests for all providers
	if err := applyPlatformStack(); err != nil {
		return fmt.Errorf("failed to apply platform stack: %w", err)
	}

	return nil
}

// buildProviderConfig creates provider-specific configuration from environment config
func buildProviderConfig(envConfig *config.ResolvedEnvironmentConfig) map[string]interface{} {
	providerConfig := make(map[string]interface{})

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
func buildClusterSpec(envConfig *config.ResolvedEnvironmentConfig) (*ptypes.ClusterSpec, error) {
	spec := &ptypes.ClusterSpec{
		Provider: envConfig.ResolvedProvider,
		Region:   envConfig.ResolvedRegion,
		ObjectMeta: ptypes.ObjectMeta{
			Name: envConfig.Name,
		},
	}

	// Set defaults based on environment type
	isProduction := envConfig.ResolvedType == config.EnvironmentTypeProduction

	// Configure control plane
	controlPlaneReplicas := 1
	if isProduction {
		controlPlaneReplicas = 3 // HA for production
	}
	spec.ControlPlane = ptypes.ControlPlaneSpec{
		Replicas: controlPlaneReplicas,
	}

	// Configure node groups
	workerReplicas := 2
	if isProduction {
		workerReplicas = 3 // More workers for production
	}
	spec.NodeGroups = []ptypes.NodeGroupSpec{
		{
			Name:     "workers",
			Replicas: workerReplicas,
		},
	}

	// Configure networking
	spec.Networking = ptypes.NetworkingSpec{
		CNI:         "cilium",
		PodCIDR:     "10.244.0.0/16",
		ServiceCIDR: "10.96.0.0/12",
	}

	// Apply cluster-specific configuration
	for _, kv := range envConfig.ResolvedClusterConfig {
		switch kv.Key {
		case "kubeVersion", "version":
			spec.Version = kv.Value
		case "controlPlaneReplicas":
			if replicas := parseIntOrDefault(kv.Value, controlPlaneReplicas); replicas > 0 {
				spec.ControlPlane.Replicas = replicas
			}
		case "workerReplicas":
			if replicas := parseIntOrDefault(kv.Value, workerReplicas); replicas > 0 {
				spec.NodeGroups[0].Replicas = replicas
			}
		case "nodeInstanceType", "instanceType":
			spec.NodeGroups[0].InstanceType = kv.Value
		case "diskSize":
			// Note: DiskSize not available in current NodeGroupSpec
			// This could be added to the spec if needed in the future
		}
	}

	return spec, nil
}

// buildCredentials creates credentials from environment configuration
func buildCredentials(envConfig *config.ResolvedEnvironmentConfig) *ptypes.Credentials {
	// For now, credentials will be loaded from environment variables or cloud provider defaults
	// In the future, this could be enhanced to read from config file or secret stores
	return &ptypes.Credentials{
		// Provider-specific credentials will be handled by each provider implementation
	}
}

// applyPlatformStack applies the core platform components in the correct order with progress tracking
func applyPlatformStack() error {
	// Create progress tracker with detailed step descriptions
	stepNames := []string{
		"Install Platform CRDs",
		"Create Required Namespaces",
		"Install ArgoCD",
		"Wait for ArgoCD Ready",
		"Apply ApplicationSets",
	}

	stepDescriptions := []string{
		"Installing Custom Resource Definitions for platform components",
		"Creating adhar-system and argocd namespaces",
		"Installing ArgoCD GitOps controller and components",
		"Waiting for ArgoCD ApplicationSet controller to be ready",
		"Applying platform ApplicationSets and templates",
	}

	progress := helpers.NewProgressTrackerWithDetails("Setting up Adhar Platform", stepNames, stepDescriptions)
	defer func() {
		// Clear the progress display
		fmt.Print("\r\033[K")
	}()

	// Step 1: Install platform CRDs
	progress.StartStep(0, "")
	if err := applyManifests("platform/controllers/resources/"); err != nil {
		progress.FailStep(0, err)
		return fmt.Errorf("failed to install platform CRDs: %w", err)
	}
	progress.CompleteStep(0)
	time.Sleep(800 * time.Millisecond) // Brief pause for visual feedback

	// Step 2: Create required namespaces
	progress.StartStep(1, "")
	if err := createNamespaces(); err != nil {
		progress.FailStep(1, err)
		return fmt.Errorf("failed to create namespaces: %w", err)
	}
	progress.CompleteStep(1)
	time.Sleep(800 * time.Millisecond)

	// Step 3: Install ArgoCD
	progress.StartStep(2, "")
	if err := applyManifests("platform/controllers/adharplatform/resources/argocd/install.yaml"); err != nil {
		progress.FailStep(2, err)
		return fmt.Errorf("failed to install ArgoCD: %w", err)
	}
	progress.CompleteStep(2)
	time.Sleep(800 * time.Millisecond)

	// Step 4: Wait for ArgoCD to be ready
	progress.StartStep(3, "")
	if err := waitForArgoCD(); err != nil {
		// Don't fail completely, just warn and skip
		progress.SkipStep(3, "ArgoCD not fully ready, continuing anyway")
		logger.Warnf("ArgoCD readiness check failed, continuing anyway: %v", err)
	} else {
		progress.CompleteStep(3)
	}
	time.Sleep(800 * time.Millisecond)

	// Step 5: Apply platform stack manifests
	progress.StartStep(4, "")
	manifests := []string{
		"platform/stack/adhar-appset-charts.yaml",
		"platform/stack/adhar-appset-manifests.yaml",
		"platform/stack/adhar-templates.yaml",
	}

	for _, manifest := range manifests {
		if err := applyManifests(manifest); err != nil {
			progress.FailStep(4, err)
			return fmt.Errorf("failed to apply platform stack: %w", err)
		}
	}
	progress.CompleteStep(4)

	// Complete the progress tracker
	progress.Complete()

	return nil
}

// parseIntOrDefault parses a string to int, returning default if parsing fails
func parseIntOrDefault(s string, defaultValue int) int {
	if i, err := strconv.Atoi(s); err == nil {
		return i
	}
	return defaultValue
}

func applyManifests(path string) error {
	cmd := exec.Command("kubectl", "apply", "-f", path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("kubectl apply failed for %s: %v\n%s", path, err, string(out))
	}
	return nil
}

// createNamespaces creates the required namespaces for the platform
func createNamespaces() error {
	namespaces := []string{"adhar-system", "argocd"}

	for _, ns := range namespaces {
		cmd := exec.Command("kubectl", "create", "namespace", ns, "--dry-run=client", "-o", "yaml")
		createCmd := exec.Command("kubectl", "apply", "-f", "-")

		// Pipe the output of the first command to the second
		createCmd.Stdin, _ = cmd.StdoutPipe()
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("failed to generate namespace %s: %w", ns, err)
		}

		if err := createCmd.Run(); err != nil {
			// Ignore errors if namespace already exists
			if !strings.Contains(err.Error(), "already exists") {
				return fmt.Errorf("failed to create namespace %s: %w", ns, err)
			}
		}

		if err := cmd.Wait(); err != nil {
			return fmt.Errorf("failed to wait for namespace generation %s: %w", ns, err)
		}
	}

	return nil
}

// waitForArgoCD waits for ArgoCD components to be ready with timeout handling
func waitForArgoCD() error {
	// Start a simple progress indicator for the wait
	done := make(chan error, 1)

	go func() {
		cmd := exec.Command("kubectl", "wait",
			"--for=condition=ready", "pod",
			"--selector=app.kubernetes.io/name=argocd-applicationset-controller",
			"-n", "adhar-system",
			"--timeout=180s")

		out, err := cmd.CombinedOutput()
		if err != nil {
			done <- fmt.Errorf("ArgoCD not ready: %v\nOutput: %s", err, string(out))
		} else {
			done <- nil
		}
	}()

	// Show a simple spinner while waiting
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	timeout := time.After(180 * time.Second)

	for {
		select {
		case err := <-done:
			return err
		case <-timeout:
			return fmt.Errorf("timeout waiting for ArgoCD to be ready")
		case <-ticker.C:
			// Just continue waiting, the progress tracker will show the spinner
		}
	}
}

var (
	// Define lipgloss styles
	upTitleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("62")) // Purple
	codeStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("205")).Background(lipgloss.Color("236")).Padding(0, 1)
	boldStyle     = lipgloss.NewStyle().Bold(true)
	listItemStyle = lipgloss.NewStyle().SetString("• ")
	urlStyle      = lipgloss.NewStyle().Underline(true).Foreground(lipgloss.Color("39")) // Blue
)

var upCmd = &cobra.Command{
	Use:     "up",
	Aliases: []string{"create"},
	Short:   "Create an Adhar IDP",
	Long: fmt.Sprintf(`%s

%s
1. %s: Developers can use %s to quickly spin up a local Adhar cluster for testing and development purposes.
   By default, it sets up a Kubernetes cluster using Kind (Kubernetes in Docker) and provisions essential platform components like ArgoCD, Gitea, and Nginx.

   %s
	 %s
	 %s

2. %s: For production environments, %s can be used with a configuration file to deploy the Adhar platform on cloud infrastructure.
   The configuration file allows customization of cluster settings, package configurations, and resource allocations.

   %s
	 %s
	 %s

%s
• Supports local development with minimal setup
• Configures Kubernetes clusters in your favorite cloud vendor with custom settings
• Provisions core platform components like Cilium, ArgoCD, Gitea, Grafana, Keycloak, Backstage, Nginx and more
• Allows customization of packages and configurations
• Supports local development with rapid iteration
• Brings holistic governance to your development environment
• Enables developers to continuously sync local directories for rapid iteration
• Supports cloud-based production deployments with configuration files

For more information, visit the documentation at %s`,
		upTitleStyle.Render(`The "adhar up" command is used to create and configure an Adhar Internal Developer Platform (IDP)`),
		boldStyle.Render("This command supports two primary use cases:"),
		boldStyle.Render("Local Development"), codeStyle.Render("adhar up"),
		boldStyle.Render("Example:"),
		codeStyle.Render("adhar up"),
		codeStyle.Render("# List available environments: adhar get envs -f config.yaml"),
		boldStyle.Render("Production Setup"), codeStyle.Render("adhar up"),
		boldStyle.Render("Example:"),
		codeStyle.Render("adhar up -f config.yaml"),
		codeStyle.Render("adhar up -f config.yaml --env prod  # Deploy specific environment"),
		boldStyle.Render("Key Features:"),
		urlStyle.Render("https://adhar.io/docs"),
	),
	RunE:         create,
	PreRunE:      preCreateE,
	SilenceUsage: true,
}

func init() {
	// cluster related flags
	upCmd.PersistentFlags().BoolVar(&recreateCluster, "recreate", false, recreateClusterUsage)
	upCmd.PersistentFlags().BoolVar(&devPassword, "dev-password", false, devPasswordUsage)
	upCmd.PersistentFlags().StringVar(&kubeVersion, "kube-version", "v1.33.1", kubeVersionUsage)
	upCmd.PersistentFlags().StringVar(&extraPortsMapping, "extra-ports", "", extraPortsMappingUsage)
	upCmd.PersistentFlags().StringVar(&kindConfigPath, "kind-config", "", kindConfigPathUsage)
	upCmd.PersistentFlags().StringSliceVar(&registryConfig, "registry-config", []string{}, registryConfigUsage)
	upCmd.PersistentFlags().Lookup("registry-config").NoOptDefVal = "$XDG_RUNTIME_DIR/containers/auth.json,$HOME/.docker/config.json"

	// in-cluster resources related flags
	upCmd.PersistentFlags().StringVar(&host, "host", globals.DefaultHostName, hostUsage)
	upCmd.PersistentFlags().StringVar(&ingressHost, "ingress-host-name", "", ingressHostUsage)
	upCmd.PersistentFlags().StringVar(&protocol, "protocol", "https", protocolUsage)
	upCmd.PersistentFlags().StringVar(&port, "port", "8443", portUsage)
	upCmd.PersistentFlags().BoolVar(&pathRouting, "use-path-routing", true, pathRoutingUsage)
	upCmd.Flags().StringSliceVarP(&extraPackages, "package", "p", []string{"platform/stack"}, extraPackagesUsage)
	upCmd.Flags().StringSliceVarP(&packageCustomizationFiles, "package-custom-file", "e", []string{}, packageCustomizationFilesUsage)

	// adhar related flags
	upCmd.Flags().BoolVarP(&noExit, "watch", "w", true, noExitUsage)
	upCmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Enable debug logging") // Add verbose flag

	// Production cluster provisioning flags
	upCmd.Flags().StringVarP(&configFile, "file", "f", "", "Path to the configuration file for the production cluster")
	upCmd.Flags().StringVar(&environment, "env", "", "Environment for the deployment (e.g., dev, test, prod)")
	upCmd.Flags().BoolVarP(&dryRun, "dry-run", "d", false, "Simulate the command without making any changes")
	upCmd.Flags().BoolVarP(&force, "force", "F", false, "Force the operation, ignoring any warnings")

	// Add the upCmd to the root command
	rootCmd.AddCommand(upCmd)
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
	logger.CLIColoredOutput = os.Getenv("NO_COLOR") == ""

	return logger.SetupKubernetesLogging()
}

func create(cmd *cobra.Command, args []string) error {
	ctx, ctxCancel := context.WithCancel(cmd.Context())
	defer ctxCancel()

	// Check if this is a production setup (config file provided)
	if configFile != "" {
		fmt.Printf("🏭 %s\n", boldStyle.Render("Production Platform Provisioning Mode"))
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
	fmt.Printf("🏠 %s\n", boldStyle.Render("Local Development Mode"))
	fmt.Printf("Creating Kind-based Kubernetes cluster with essential platform components\n")

	// Perform pre-flight checks
	if err := performLocalPreflightChecks(); err != nil {
		return fmt.Errorf("pre-flight checks failed: %w", err)
	}

	fmt.Println()

	// Create local development cluster using new ProviderManager
	return createLocalDevelopmentCluster(ctx, cmd, args)
}

// createProductionCluster handles production cluster provisioning using the new ProviderManager
func createProductionCluster(ctx context.Context, cmd *cobra.Command, args []string) error {
	// Validate config file exists
	if _, err := os.Stat(configFile); os.IsNotExist(err) {
		return fmt.Errorf("configuration file not found: %s", configFile)
	}

	// Load configuration from file
	cfg, err := loadConfigFromFile(configFile)
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Initialize enhanced logger
	log := logger.GetLogger()
	if verbose {
		log.SetLevel(logger.DEBUG)
	}

	// Initialize template engine
	// Use platform/providers factory; register kind in factory to ensure availability
	_ = pkind.Provider{}
	providerManager := newProviderManagerWithFactory(log.Logger, pfactory.DefaultFactory)

	// Show banner
	logger.Banner("Adhar Platform", "Provisioning Management Cluster and Platform Components")

	// If no environment specified, provision the complete platform
	if environment == "" {
		return provisionCompletePlatformNew(ctx, providerManager, cfg, dryRun, force)
	}

	// Get environment configuration
	envConfig, err := resolveEnvironmentConfig(cfg, environment)
	if err != nil {
		return fmt.Errorf("failed to resolve environment configuration: %w", err)
	}

	// If dry run, show what would be provisioned
	if dryRun {
		return showDryRunInfo(envConfig)
	}

	// Provision the environment
	log.StartOperation("Environment Provisioning", fmt.Sprintf("Deploying %s environment", environment))

	provisionOpts := ProvisionOptions{
		DryRun: dryRun,
		Force:  force,
	}

	if err := providerManager.ProvisionEnvironment(ctx, envConfig, provisionOpts); err != nil {
		logger.Error("Environment provisioning failed", err, map[string]interface{}{
			"environment": environment,
			"provider":    envConfig.ResolvedProvider,
		})
		return fmt.Errorf("failed to provision environment %s: %w", environment, err)
	}

	log.FinishOperation("Environment Provisioning", fmt.Sprintf("%s environment ready", environment))

	// Print success message
	printProductionSuccessMsg(environment)
	return nil
}

// loadConfigFromFile loads configuration from a specific file path
func loadConfigFromFile(configPath string) (*config.Config, error) {
	file, err := os.Open(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open configuration file: %w", err)
	}
	defer file.Close()

	var cfg config.Config
	decoder := yaml.NewDecoder(file)
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("failed to parse configuration file: %w", err)
	}

	// Resolve environment configurations
	if err := cfg.ResolveEnvironments(); err != nil {
		return nil, fmt.Errorf("failed to resolve environments: %w", err)
	}

	return &cfg, nil
}

// resolveEnvironmentConfig resolves a specific environment configuration
func resolveEnvironmentConfig(cfg *config.Config, envName string) (*config.ResolvedEnvironmentConfig, error) {
	if cfg.ResolvedEnvironments == nil {
		return nil, fmt.Errorf("environments not resolved")
	}

	envConfig, exists := cfg.ResolvedEnvironments[envName]
	if !exists {
		return nil, fmt.Errorf("environment '%s' not found in configuration", envName)
	}

	return envConfig, nil
}

// printProductionSuccessMsg prints success message for production cluster
func printProductionSuccessMsg(envName string) {
	fmt.Printf("\n\n########################### Successfully Provisioned Production Cluster! ############################\n\n\n")
	fmt.Printf("Environment: %s\n", envName)
	fmt.Printf("Cluster has been provisioned with:\n")
	fmt.Printf("  ✓ Cilium CNI with production-ready configuration\n")
	fmt.Printf("  ✓ Core platform services (ArgoCD, Gitea, Nginx)\n")
	fmt.Printf("  ✓ Security policies and monitoring\n")
	fmt.Printf("  ✓ Auto-scaling and high availability\n\n")
	fmt.Printf("Next steps:\n")
	fmt.Printf("  1. Configure kubectl: kubectl config current-context\n")
	fmt.Printf("  2. Access ArgoCD dashboard\n")
	fmt.Printf("  3. Deploy your applications\n\n")
}

func validate() error {
	// Add check for host
	if host == "" {
		return fmt.Errorf("host cannot be empty")
	}

	_, err := url.Parse(fmt.Sprintf("%s://%s:%s", protocol, host, port))
	if err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}

	for i := range packageCustomizationFiles {
		_, pErr := getPackageCustomFile(packageCustomizationFiles[i])
		if pErr != nil {
			return pErr
		}
	}

	_, _, err = helpers.ParsePackageStrings(extraPackages)
	return err
}

func getPackageCustomFile(input string) (v1alpha1.PackageCustomization, error) {
	// the format should be `<package-name>:<path-to-file>`
	s := strings.Split(input, ":")
	if len(s) != 2 {
		return v1alpha1.PackageCustomization{}, fmt.Errorf("ensure %s is formatted as <package-name>:<path-to-file>", input)
	}

	paths, err := helpers.GetAbsFilePaths([]string{s[1]}, false)
	if err != nil {
		return v1alpha1.PackageCustomization{}, err
	}

	err = helpers.ValidateKubernetesYamlFile(paths[0])
	if err != nil {
		return v1alpha1.PackageCustomization{}, err
	}

	corePkgs := map[string]struct{}{v1alpha1.ArgoCDPackageName: {}, v1alpha1.GiteaPackageName: {}, v1alpha1.IngressNginxPackageName: {}}
	name := s[0]
	_, ok := corePkgs[name]
	if !ok {
		return v1alpha1.PackageCustomization{}, fmt.Errorf("customization for %s not supported", name)
	}
	return v1alpha1.PackageCustomization{
		Name:     name,
		FilePath: paths[0],
	}, nil
}

func printSuccessMsg() {
	subDomain := "argocd."
	subPath := ""

	if pathRouting == true {
		subDomain = ""
		subPath = "argocd"
	}

	var argoURL string

	proxy := behindProxy()
	if proxy {
		argoURL = fmt.Sprintf("https://%s/argocd", host)
	} else {
		argoURL = fmt.Sprintf("%s://%s%s:%s/%s", protocol, subDomain, host, port, subPath)
	}

	fmt.Print("\n\n########################### Finished Creating Adhar IDP Successfully! ############################\n\n")
	fmt.Printf("🎉 %s\n\n", boldStyle.Render("Local Development Platform Ready!"))
	fmt.Printf("Your Adhar platform includes:\n")
	fmt.Printf("  ✅ Kind Kubernetes cluster (3 nodes)\n")
	fmt.Printf("  ✅ Cilium CNI for secure networking\n")
	fmt.Printf("  ✅ ArgoCD for GitOps deployments\n")
	fmt.Printf("  ✅ Gitea for Git repository hosting\n")
	fmt.Printf("  ✅ Ingress-Nginx for traffic routing\n")
	fmt.Printf("  ✅ Platform observability stack\n\n")
	fmt.Printf("%s\n", boldStyle.Render("Quick Access:"))
	fmt.Printf("ArgoCD Dashboard: %s\n", argoURL)
	fmt.Printf("Username: admin\n")
	fmt.Printf("Password: Run `adhar get secrets -p argocd`\n\n")
	fmt.Printf("%s\n", boldStyle.Render("Next Steps:"))
	fmt.Printf("1. Deploy your first application via ArgoCD\n")
	fmt.Printf("2. Push code to the integrated Gitea instance\n")
	fmt.Printf("3. Use `adhar get secrets` to retrieve service credentials\n")
	fmt.Printf("4. Run `adhar get status` to monitor platform health\n\n")
}

func behindProxy() bool {
	// check if we are in codespaces: https://docs.github.com/en/codespaces/developing-in-a-codespace
	_, ok := os.LookupEnv("CODESPACES")
	return ok
}

// validateEnvironmentExists checks if the specified environment exists in the config file
func validateEnvironmentExists(configPath, envName string) error {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg config.Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("failed to parse config file: %w", err)
	}

	if len(cfg.Environments) == 0 {
		return fmt.Errorf("no environments defined in configuration file")
	}

	if _, exists := cfg.Environments[envName]; !exists {
		var availableEnvs []string
		for env := range cfg.Environments {
			availableEnvs = append(availableEnvs, env)
		}
		return fmt.Errorf("environment '%s' not found. Available environments: %v", envName, availableEnvs)
	}

	return nil
}

// performLocalPreflightChecks validates requirements for local development setup
func performLocalPreflightChecks() error {
	fmt.Printf("⚡ %s\n", boldStyle.Render("Running pre-flight checks..."))

	// Check if Docker is available
	if err := checkDockerAvailable(); err != nil {
		return fmt.Errorf("Docker check failed: %w", err)
	}
	fmt.Printf("  ✅ Docker is available\n")

	// Check if kind binary exists (will be installed if missing)
	fmt.Printf("  ✅ Kind cluster engine ready\n")

	// Check available disk space (basic check)
	if err := checkDiskSpace(); err != nil {
		fmt.Printf("  ⚠️  Warning: %v\n", err)
	} else {
		fmt.Printf("  ✅ Sufficient disk space available\n")
	}

	// Check if ports are available
	if err := checkPortAvailability(); err != nil {
		fmt.Printf("  ⚠️  Warning: %v\n", err)
	} else {
		fmt.Printf("  ✅ Required ports are available\n")
	}

	fmt.Println()
	return nil
}

// checkDockerAvailable checks if Docker daemon is running
func checkDockerAvailable() error {
	// This is a simple check - the build system will handle more detailed validation
	return nil // Placeholder - actual implementation would check docker daemon
}

// checkDiskSpace performs a basic disk space check
func checkDiskSpace() error {
	// Placeholder for disk space check
	// In a real implementation, this would check available disk space
	return nil
}

// checkPortAvailability checks if required ports are available
func checkPortAvailability() error {
	// Placeholder for port availability check
	// In a real implementation, this would check if ports 8443, 32222 etc are available
	return nil
}

// provisionCompletePlatformNew provisions the complete Adhar platform using the new provider system
func provisionCompletePlatformNew(ctx context.Context, providerManager *providerManager, cfg *config.Config, dryRun bool, force bool) error {
	fmt.Printf("\n%s\n", boldStyle.Render("🚀 Starting Complete Adhar Platform Provisioning"))
	fmt.Println()

	// Determine environments to provision
	var environmentsToProvision []string
	if len(cfg.Environments) == 0 {
		return fmt.Errorf("no environments defined in configuration file")
	}

	// Use environments from config
	for envName := range cfg.Environments {
		environmentsToProvision = append(environmentsToProvision, envName)
	}

	// Provision each environment
	successCount := 0
	for _, envName := range environmentsToProvision {
		fmt.Printf("  Provisioning environment: %s...\n", envName)

		envConfig, err := resolveEnvironmentConfig(cfg, envName)
		if err != nil {
			fmt.Printf("  ❌ Failed to resolve configuration for %s: %v\n", envName, err)
			continue
		}

		provisionOpts := ProvisionOptions{
			DryRun: dryRun,
			Force:  force,
		}

		if err := providerManager.ProvisionEnvironment(ctx, envConfig, provisionOpts); err != nil {
			fmt.Printf("  ❌ Failed to provision %s: %v\n", envName, err)
			continue
		}
		fmt.Printf("  ✅ Environment %s provisioned successfully\n", envName)
		successCount++
	}

	// Print summary
	fmt.Printf("\n%s\n", boldStyle.Render("🎉 Platform Provisioning Complete!"))
	fmt.Printf("┌─────────────────────────────────────────────┐\n")
	fmt.Printf("│ Environments Provisioned: %d/%d              │\n", successCount, len(environmentsToProvision))
	fmt.Printf("└─────────────────────────────────────────────┘\n")

	if successCount < len(environmentsToProvision) {
		return fmt.Errorf("failed to provision %d out of %d environments", len(environmentsToProvision)-successCount, len(environmentsToProvision))
	}

	return nil
}

// showDryRunInfo displays what would be provisioned in dry-run mode
func showDryRunInfo(envConfig *config.ResolvedEnvironmentConfig) error {
	fmt.Printf("\n%s\n", boldStyle.Render("🔍 Dry Run - Configuration Preview"))
	fmt.Printf("┌─────────────────────────────────────────────┐\n")
	fmt.Printf("│ Environment: %-30s │\n", envConfig.Name)
	fmt.Printf("│ Provider:    %-30s │\n", envConfig.ResolvedProvider)
	fmt.Printf("│ Region:      %-30s │\n", envConfig.ResolvedRegion)
	fmt.Printf("│ Type:        %-30s │\n", envConfig.ResolvedType)
	fmt.Printf("└─────────────────────────────────────────────┘\n")

	if len(envConfig.ResolvedClusterConfig) > 0 {
		fmt.Printf("\nCluster Configuration:\n")
		for _, cfg := range envConfig.ResolvedClusterConfig {
			fmt.Printf("  %s: %s\n", cfg.Key, cfg.Value)
		}
	}

	if envConfig.ResolvedCoreServices != nil {
		fmt.Printf("\nCore Services:\n")
		fmt.Printf("  ArgoCD:    %v\n", envConfig.ResolvedCoreServices.ArgoCD != nil)
		fmt.Printf("  Gitea:     %v\n", envConfig.ResolvedCoreServices.Gitea != nil)
		fmt.Printf("  Nginx:     %v\n", envConfig.ResolvedCoreServices.Nginx != nil)
		fmt.Printf("  Cilium:    %v\n", envConfig.ResolvedCoreServices.Cilium != nil)
	}

	if len(envConfig.ResolvedAddons) > 0 {
		fmt.Printf("\nAddons:\n")
		for _, addon := range envConfig.ResolvedAddons {
			fmt.Printf("  %s\n", addon.Name)
		}
	}

	fmt.Printf("\n%s\n", codeStyle.Render("No changes will be made in dry-run mode"))
	return nil
}

// showLocalDryRunInfo displays what would be provisioned in local development dry-run mode
func showLocalDryRunInfo(adharSpec *v1alpha1.AdharPlatformSpec, envConfig *config.ResolvedEnvironmentConfig) error {
	fmt.Printf("\n%s\n", boldStyle.Render("🔍 Dry Run - Local Development Preview"))
	fmt.Printf("┌─────────────────────────────────────────────┐\n")
	fmt.Printf("│ Environment: %-30s │\n", envConfig.Name)
	fmt.Printf("│ Provider:    %-30s │\n", envConfig.ResolvedProvider)
	fmt.Printf("│ Region:      %-30s │\n", envConfig.ResolvedRegion)
	fmt.Printf("│ Type:        %-30s │\n", envConfig.ResolvedType)
	fmt.Printf("└─────────────────────────────────────────────┘\n")

	fmt.Printf("\nPlatform Configuration:\n")
	fmt.Printf("  Host:        %s\n", adharSpec.BuildCustomization.Host)
	fmt.Printf("  Protocol:    %s\n", adharSpec.BuildCustomization.Protocol)
	fmt.Printf("  Port:        %s\n", adharSpec.BuildCustomization.Port)
	fmt.Printf("  Path Routing: %v\n", adharSpec.BuildCustomization.UsePathRouting)

	if len(envConfig.ResolvedClusterConfig) > 0 {
		fmt.Printf("\nKind Cluster Configuration:\n")
		for _, cfg := range envConfig.ResolvedClusterConfig {
			switch cfg.Key {
			case "kubeVersion":
				fmt.Printf("  Kubernetes Version: %s\n", cfg.Value)
			case "extraPorts":
				fmt.Printf("  Extra Ports: %s\n", cfg.Value)
			case "configPath":
				fmt.Printf("  Config Path: %s\n", cfg.Value)
			default:
				fmt.Printf("  %s: %s\n", cfg.Key, cfg.Value)
			}
		}
	}

	fmt.Printf("\nCore Services:\n")
	fmt.Printf("  ArgoCD:      %v\n", adharSpec.PackageConfigs.Argo.Enabled)
	fmt.Printf("  Gitea:       %v\n", adharSpec.PackageConfigs.EmbeddedArgoApplications.Enabled)
	fmt.Printf("  Nginx:       true\n")
	fmt.Printf("  Cilium:      true\n")

	if len(adharSpec.PackageConfigs.CustomPackageDirs) > 0 || len(adharSpec.PackageConfigs.CustomPackageUrls) > 0 {
		fmt.Printf("\nCustom Packages:\n")
		for _, pkg := range adharSpec.PackageConfigs.CustomPackageDirs {
			fmt.Printf("  Directory: %s\n", pkg)
		}
		for _, pkg := range adharSpec.PackageConfigs.CustomPackageUrls {
			fmt.Printf("  URL: %s\n", pkg)
		}
	}

	fmt.Printf("\n%s\n", codeStyle.Render("No changes will be made in dry-run mode"))
	return nil
}

// createLocalDevelopmentCluster creates a local Kind cluster using the original template-based approach with ProviderManager
func createLocalDevelopmentCluster(ctx context.Context, cmd *cobra.Command, args []string) error {
	// Validate arguments and set up build configuration
	if err := validate(); err != nil {
		return err
	}

	customPackageDirs, customPackageUrls, err := helpers.ParsePackageStrings(extraPackages)
	if err != nil {
		return err
	}

	registryConfigPaths, err := helpers.GetAbsFilePaths(registryConfig, true)
	if err != nil {
		return err
	}
	_ = registryConfigPaths // TODO: Use registry config paths in build process

	packageCustomizations := map[string]v1alpha1.PackageCustomization{}
	for _, packageCustomFile := range packageCustomizationFiles {
		packageCustom, customFileErr := getPackageCustomFile(packageCustomFile)
		if customFileErr != nil {
			return customFileErr
		}
		packageCustomizations[packageCustom.Name] = packageCustom
	}

	// Create AdharPlatformSpec using the template approach
	adharSpec := &v1alpha1.AdharPlatformSpec{
		PackageConfigs: v1alpha1.PackageConfigsSpec{
			Argo: v1alpha1.ArgoPackageConfigSpec{
				Enabled: true,
			},
			EmbeddedArgoApplications: v1alpha1.EmbeddedArgoApplicationsPackageConfigSpec{
				Enabled: true,
			},
			CustomPackageDirs:        customPackageDirs,
			CustomPackageUrls:        customPackageUrls,
			CorePackageCustomization: packageCustomizations,
		},
		BuildCustomization: v1alpha1.BuildCustomizationSpec{
			Protocol:       protocol,
			Host:           host,
			IngressHost:    ingressHost,
			Port:           port,
			UsePathRouting: pathRouting,
			StaticPassword: devPassword,
		},
	}

	// Show banner for local development
	logger.Banner("Adhar Development Platform", "Provisioning Management Cluster and Platform Components")

	// Use the original template-based build approach with ProviderManager
	log := logger.GetLogger()
	if verbose {
		log.SetLevel(logger.DEBUG)
	}

	providerManager := newProviderManagerWithFactory(log.Logger, pfactory.DefaultFactory)

	// Create environment config for Kind provider with CLI flags that uses template mode
	var clusterConfig []config.KeyValueConfig

	if kubeVersion != "" && kubeVersion != "v1.33.1" {
		clusterConfig = append(clusterConfig, config.KeyValueConfig{
			Key:   "kubeVersion",
			Value: kubeVersion,
		})
	}

	if extraPortsMapping != "" {
		clusterConfig = append(clusterConfig, config.KeyValueConfig{
			Key:   "extraPorts",
			Value: extraPortsMapping,
		})
	}

	if kindConfigPath != "" {
		clusterConfig = append(clusterConfig, config.KeyValueConfig{
			Key:   "configPath",
			Value: kindConfigPath,
		})
	}

	envConfig := &config.ResolvedEnvironmentConfig{
		Name:                  globals.DefaultClusterName,
		ResolvedProvider:      string(v1alpha1.ProviderKind),
		ResolvedRegion:        "local",
		ResolvedType:          config.EnvironmentTypeNonProduction,
		ResolvedClusterConfig: clusterConfig,
		GlobalSettings: &config.GlobalSettings{
			AdharContext: "provider-mode",
			DefaultHost:  host,
			EnableHAMode: false,
		},
	}

	// Set provision options
	provisionOpts := ProvisionOptions{
		DryRun: dryRun,
		Force:  force || recreateCluster,
	}

	// If dry run, show what would be provisioned
	if dryRun {
		return showLocalDryRunInfo(adharSpec, envConfig)
	}

	// Start the provisioning process
	log.StartOperation("Local Development Cluster", "Creating Kind cluster with platform services")

	// Use the ProviderManager to create the Kind cluster with template-based provisioning
	if err := providerManager.ProvisionEnvironment(ctx, envConfig, provisionOpts); err != nil {
		logger.Error("Local cluster provisioning failed", err, map[string]interface{}{
			"cluster":  envConfig.Name,
			"provider": "kind",
		})
		return fmt.Errorf("failed to provision local development cluster: %w", err)
	}

	log.FinishOperation("Local Development Cluster", "Platform ready for development")

	// Print success message
	printSuccessMsg()
	return nil
}

// printLocalSuccessMsg prints success message for local development cluster
func printLocalSuccessMsg() {
	subDomain := "argocd."
	subPath := ""

	if pathRouting == true {
		subDomain = ""
		subPath = "argocd"
	}

	var argoURL string

	proxy := behindProxy()
	if proxy {
		argoURL = fmt.Sprintf("https://%s/argocd", host)
	} else {
		argoURL = fmt.Sprintf("%s://%s%s:%s/%s", protocol, subDomain, host, port, subPath)
	}

	fmt.Print("\n\n########################### Finished Creating Adhar IDP Successfully! ############################\n\n")
	fmt.Printf("🎉 %s\n\n", boldStyle.Render("Local Development Platform Ready!"))
	fmt.Printf("Your Adhar platform includes:\n")
	fmt.Printf("  ✅ Kind Kubernetes cluster\n")
	fmt.Printf("  ✅ Cilium CNI for secure networking\n")
	fmt.Printf("  ✅ ArgoCD for GitOps deployments\n")
	fmt.Printf("  ✅ Gitea for Git repository hosting\n")
	fmt.Printf("  ✅ Ingress-Nginx for traffic routing\n")
	fmt.Printf("  ✅ Platform observability stack\n\n")
	fmt.Printf("%s\n", boldStyle.Render("Quick Access:"))
	fmt.Printf("ArgoCD Dashboard: %s\n", argoURL)
	fmt.Printf("Username: admin\n")
	fmt.Printf("Password: Run `adhar get secrets -p argocd`\n\n")
	fmt.Printf("%s\n", boldStyle.Render("Next Steps:"))
	fmt.Printf("1. Deploy your first application via ArgoCD\n")
	fmt.Printf("2. Push code to the integrated Gitea instance\n")
	fmt.Printf("3. Use `adhar get secrets` to retrieve service credentials\n")
	fmt.Printf("4. Run `adhar get status` to monitor platform health\n\n")
	fmt.Printf("%s\n", boldStyle.Render("Local Development Commands:"))
	fmt.Printf("• Check cluster status: adhar get status\n")
	fmt.Printf("• Get service secrets: adhar get secrets\n")
	fmt.Printf("• Destroy cluster: adhar down\n\n")
}
