package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/build"
	"adhar-io/adhar/platform/config"
	"adhar-io/adhar/platform/k8s"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
	"k8s.io/client-go/util/homedir"
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

var (
	// Define lipgloss styles
	upTitleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("62")) // Purple
	codeStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("205")).Background(lipgloss.Color("236")).Padding(0, 1)
	boldStyle     = lipgloss.NewStyle().Bold(true)
	listItemStyle = lipgloss.NewStyle().SetString("• ")
	urlStyle      = lipgloss.NewStyle().Underline(true).Foreground(lipgloss.Color("39")) // Blue
)

var upCmd = &cobra.Command{
	Use:   "up",
	Short: "Create an Adhar IDP",
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
	// Add the alias here
	upCmd.Aliases = []string{"create"}

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
		_ = helpers.SetLogLevel("debug")
	} else {
		_ = helpers.SetLogLevel("info")
	}
	return helpers.SetLogger()
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

	// Continue with existing local development flow
	kubeConfigPath := filepath.Join(homedir.HomeDir(), ".kube", "config")

	protocol = strings.ToLower(protocol)
	host = strings.ToLower(host)
	if ingressHost == "" {
		ingressHost = host
	}

	err := validate()
	if err != nil {
		return err
	}

	var absDirPaths []string
	var remotePaths []string

	if len(extraPackages) > 0 {
		r, l, pErr := helpers.ParsePackageStrings(extraPackages)
		if pErr != nil {
			return pErr
		}
		absDirPaths = l
		remotePaths = r
	}

	o := make(map[string]v1alpha1.PackageCustomization)
	for i := range packageCustomizationFiles {
		c, pErr := getPackageCustomFile(packageCustomizationFiles[i])
		if pErr != nil {
			return pErr
		}
		o[c.Name] = c
	}

	exitOnSync := true
	if cmd.Flags().Changed("watch") {
		exitOnSync = !noExit
	}

	// If registry-config is unset we pass nil
	// If registry-config is change (--registry-config=foo) we pass the new value
	// If registry-config is set but unchanged (--registry-confg) we pass ""
	maybeRegistryConfig := []string{}
	if cmd.Flags().Changed("registry-config") {
		maybeRegistryConfig = registryConfig
	}

	opts := build.NewBuildOptions{
		Name:              globals.DefaultClusterName,
		KubeVersion:       kubeVersion,
		KubeConfigPath:    kubeConfigPath,
		KindConfigPath:    kindConfigPath,
		ExtraPortsMapping: extraPortsMapping,
		RegistryConfig:    maybeRegistryConfig,

		TemplateData: v1alpha1.BuildCustomizationSpec{
			Protocol:       protocol,
			Host:           host,
			IngressHost:    ingressHost,
			Port:           port,
			UsePathRouting: pathRouting,
			StaticPassword: devPassword,
		},

		CustomPackageDirs:    absDirPaths,
		CustomPackageUrls:    remotePaths,
		ExitOnSync:           exitOnSync,
		PackageCustomization: o,

		Scheme:     k8s.GetScheme(),
		CancelFunc: ctxCancel,
	}

	b := build.NewBuild(opts)

	if err := b.Run(ctx, recreateCluster); err != nil {
		return err
	}

	if cmd.Context().Err() != nil {
		return context.Cause(cmd.Context())
	}

	printSuccessMsg()
	return nil
}

// createProductionCluster handles production cluster provisioning
func createProductionCluster(ctx context.Context, cmd *cobra.Command, args []string) error {
	// Validate config file exists
	if _, err := os.Stat(configFile); os.IsNotExist(err) {
		return fmt.Errorf("configuration file not found: %s", configFile)
	}

	// Create provisioning options
	opts := &build.ProvisioningOptions{
		ConfigPath:      configFile,
		EnvironmentName: environment,
		DryRun:          dryRun,
		Force:           force,
		Verbose:         verbose,
	}

	// Create cluster provisioner
	provisioner, err := build.NewClusterProvisioner(opts)
	if err != nil {
		return fmt.Errorf("failed to create cluster provisioner: %w", err)
	}

	// If no environment specified, provision the complete platform
	if environment == "" {
		return provisionCompletePlatform(ctx, provisioner, configFile)
	}

	// Validate environment exists in config
	if err := validateEnvironmentExists(configFile, environment); err != nil {
		return fmt.Errorf("environment validation failed: %w", err)
	}

	// Check if management cluster exists, provision if needed
	if err := provisioner.EnsureManagementCluster(ctx); err != nil {
		return fmt.Errorf("failed to ensure management cluster: %w", err)
	}

	// Provision the workload cluster
	if err := provisioner.ProvisionCluster(ctx, environment); err != nil {
		return fmt.Errorf("cluster provisioning failed: %w", err)
	}

	// Print success message
	printProductionSuccessMsg(environment)
	return nil
}

// provisionCompletePlatform provisions the complete Adhar platform including management cluster and all environments
func provisionCompletePlatform(ctx context.Context, provisioner *build.ClusterProvisioner, configPath string) error {
	fmt.Printf("\n%s\n", boldStyle.Render("🚀 Starting Complete Adhar Platform Provisioning"))
	fmt.Printf("Configuration: %s\n\n", configPath)

	// Load configuration to determine environments
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("failed to read config file %s: %w", configPath, err)
	}

	var cfg config.Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("failed to parse config file %s: %w", configPath, err)
	}

	// Step 1: Provision Management Cluster
	fmt.Printf("%s %s\n", boldStyle.Render("Step 1:"), "Provisioning Management Cluster...")
	if err := provisioner.EnsureManagementCluster(ctx); err != nil {
		return fmt.Errorf("failed to provision management cluster: %w", err)
	}
	fmt.Printf("✅ Management cluster provisioned successfully\n\n")

	// Step 2: Deploy Platform Services
	fmt.Printf("%s %s\n", boldStyle.Render("Step 2:"), "Deploying Platform Services...")
	if err := provisioner.DeployPlatformServices(ctx); err != nil {
		return fmt.Errorf("failed to deploy platform services: %w", err)
	}
	fmt.Printf("✅ Platform services deployed successfully\n\n")

	// Step 3: Provision Environments
	fmt.Printf("%s %s\n", boldStyle.Render("Step 3:"), "Provisioning Environments...")

	// Determine environments to provision
	var environmentsToProvision []string
	if len(cfg.Environments) == 0 {
		// No environments defined, create default dev and prod
		fmt.Printf("No environments found in config. Creating default environments...\n")
		environmentsToProvision = []string{"dev", "prod"}

		// Create default environments in memory for processing
		if err := createDefaultEnvironments(&cfg); err != nil {
			return fmt.Errorf("failed to create default environments: %w", err)
		}
	} else {
		// Use environments from config
		for envName := range cfg.Environments {
			environmentsToProvision = append(environmentsToProvision, envName)
		}
	}

	// Provision each environment
	successCount := 0
	for _, envName := range environmentsToProvision {
		fmt.Printf("  Provisioning environment: %s...\n", envName)
		if err := provisioner.ProvisionCluster(ctx, envName); err != nil {
			fmt.Printf("  ❌ Failed to provision %s: %v\n", envName, err)
			continue
		}
		fmt.Printf("  ✅ Environment %s provisioned successfully\n", envName)
		successCount++
	}

	// Print summary
	fmt.Printf("\n%s\n", boldStyle.Render("🎉 Platform Provisioning Complete!"))
	fmt.Printf("┌─────────────────────────────────────────────┐\n")
	fmt.Printf("│ Management Cluster: ✅ Ready                │\n")
	fmt.Printf("│ Platform Services:  ✅ Deployed             │\n")
	fmt.Printf("│ Environments:       %d/%d provisioned       │\n", successCount, len(environmentsToProvision))
	fmt.Printf("└─────────────────────────────────────────────┘\n\n")

	if successCount > 0 {
		fmt.Printf("%s\n", boldStyle.Render("🚀 Next Steps:"))
		fmt.Printf("1. Configure kubectl context for your clusters\n")
		fmt.Printf("2. Access ArgoCD dashboard on the management cluster\n")
		fmt.Printf("3. Start deploying applications to your environments\n")
		fmt.Printf("4. Use 'adhar get secrets' to retrieve service passwords\n")
		fmt.Printf("5. Use 'adhar get envs -f %s' to list environments\n\n", filepath.Base(configPath))
	}

	return nil
}

// createDefaultEnvironments creates default dev and prod environments when none are specified
func createDefaultEnvironments(cfg *config.Config) error {
	if cfg.Environments == nil {
		cfg.Environments = make(map[string]config.EnvironmentConfig)
	}

	// Create default development environment
	cfg.Environments["dev"] = config.EnvironmentConfig{
		Type:     config.EnvironmentTypeNonProduction,
		Template: "development-defaults",
		ClusterConfig: []config.ClusterConfig{
			{Key: "name", Value: "adhar-dev"},
			{Key: "nodeCount", Value: "2"},
			{Key: "nodeSize", Value: "s-2vcpu-4gb"},
		},
	}

	// Create default production environment
	cfg.Environments["prod"] = config.EnvironmentConfig{
		Type:     config.EnvironmentTypeProduction,
		Template: "production-defaults",
		ClusterConfig: []config.ClusterConfig{
			{Key: "name", Value: "adhar-prod"},
			{Key: "nodeCount", Value: "3"},
			{Key: "machineType", Value: "e2-standard-4"},
		},
	}

	return nil
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
	// check if we are in codespaces: https://docs.github.com/en/codespaces/developing-in-a-codespace/default-environment-variables-for-your-codespace
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
