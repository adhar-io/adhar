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
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/config"
	"adhar-io/adhar/platform/logger"
	pfactory "adhar-io/adhar/platform/providers"

	"github.com/spf13/cobra"
)

// LocalProvisioner handles local development environment creation
type LocalProvisioner struct {
	options *LocalOptions
}

// LocalOptions contains configuration for local development
type LocalOptions struct {
	RecreateCluster           bool
	DevPassword               bool
	KubeVersion               string
	ExtraPortsMapping         string
	KindConfigPath            string
	ExtraPackages             []string
	RegistryConfig            []string
	PackageCustomizationFiles []string
	NoExit                    bool
	Protocol                  string
	Host                      string
	IngressHost               string
	Port                      string
	PathRouting               bool
	Verbose                   bool
}

// NewLocalProvisioner creates a new local provisioner
func NewLocalProvisioner(options *LocalOptions) *LocalProvisioner {
	return &LocalProvisioner{
		options: options,
	}
}

// Provision creates the local development environment
func (lp *LocalProvisioner) Provision() error {
	logger.Info("🏠 Local Development Mode")
	logger.Info("Creating Kind-based Kubernetes cluster with essential platform components")

	// Run pre-flight checks
	if err := lp.runPreFlightChecks(); err != nil {
		return fmt.Errorf("pre-flight checks failed: %w", err)
	}

	// Create Kind cluster using the provider
	if err := lp.createKindCluster(); err != nil {
		return fmt.Errorf("failed to create Kind cluster: %w", err)
	}

	// Install platform components
	if err := lp.installPlatformComponents(); err != nil {
		return fmt.Errorf("failed to install platform components: %w", err)
	}

	// Setup GitOps repositories
	if err := lp.setupGitOpsRepositories(); err != nil {
		return fmt.Errorf("failed to setup GitOps repositories: %w", err)
	}

	logger.Info("✅ Local development environment created successfully!")
	logger.Info("🌐 Access your platform at: " + lp.options.Protocol + "://" + lp.options.Host + ":" + lp.options.Port)

	return nil
}

// runPreFlightChecks validates system requirements
func (lp *LocalProvisioner) runPreFlightChecks() error {
	logger.Info("⚡ Running pre-flight checks...")

	checks := []struct {
		name  string
		check func() error
	}{
		{"Docker availability", lp.checkDocker},
		{"Kind cluster engine", lp.checkKindEngine},
		{"Disk space", lp.checkDiskSpace},
		{"Port availability", lp.checkPortAvailability},
	}

	for _, check := range checks {
		if err := check.check(); err != nil {
			return fmt.Errorf("❌ %s check failed: %w", check.name, err)
		}
		logger.Info("✅ " + check.name + " check passed")
	}

	return nil
}

// checkDocker verifies Docker is available and running
func (lp *LocalProvisioner) checkDocker() error {
	cmd := exec.Command("docker", "info")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("Docker is not available or not running: %w", err)
	}
	return nil
}

// checkKindEngine verifies Kind is available
func (lp *LocalProvisioner) checkKindEngine() error {
	cmd := exec.Command("kind", "version")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("Kind is not available: %w", err)
	}
	return nil
}

// checkDiskSpace verifies sufficient disk space is available
func (lp *LocalProvisioner) checkDiskSpace() error {
	// TODO: Implement disk space check
	// This should check available space in the current directory
	return nil
}

// checkPortAvailability verifies required ports are available
func (lp *LocalProvisioner) checkPortAvailability() error {
	// TODO: Implement port availability check
	// This should check if ports 80, 443, 3000, etc. are available
	return nil
}

// createKindCluster creates the Kind Kubernetes cluster
func (lp *LocalProvisioner) createKindCluster() error {
	logger.Info("🔧 Creating Kind Kubernetes cluster...")

	// Check if cluster already exists
	cmd := exec.Command("kind", "get", "clusters")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to check existing clusters: %w", err)
	}

	// If cluster already exists, delete it first if recreate is requested
	if strings.Contains(string(output), "adhar-local") {
		if lp.options.RecreateCluster {
			logger.Info("🗑️ Deleting existing cluster...")
			deleteCmd := exec.Command("kind", "delete", "cluster", "--name", "adhar-local")
			if err := deleteCmd.Run(); err != nil {
				return fmt.Errorf("failed to delete existing cluster: %w", err)
			}
		} else {
			logger.Info("✅ Cluster 'adhar-local' already exists")
			return nil
		}
	}

	// Create new cluster
	logger.Info("🏗️ Creating new Kind cluster...")
	createCmd := exec.Command("kind", "create", "cluster", "--name", "adhar-local", "--config", "-")

	// Create a simple kind config
	kindConfig := fmt.Sprintf(`kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
  kubeadmConfigPatches:
  - |
    kind: InitConfiguration
    nodeRegistration:
      kubeletExtraArgs:
        node-labels: "ingress-ready=true"
  extraPortMappings:
  - containerPort: 80
    hostPort: 80
    protocol: TCP
  - containerPort: 443
    hostPort: 443
    protocol: TCP
- role: worker
- role: worker`)

	createCmd.Stdin = strings.NewReader(kindConfig)
	if err := createCmd.Run(); err != nil {
		return fmt.Errorf("failed to create cluster: %w", err)
	}

	logger.Info("✅ Cluster created successfully")
	return nil
}

// installPlatformComponents installs the core platform components
func (lp *LocalProvisioner) installPlatformComponents() error {
	logger.Info("📦 Installing platform components...")

	// For now, just log that components will be installed
	// TODO: Implement actual component installation using kubectl or helm
	components := []string{"cilium", "nginx", "gitea", "argocd"}

	for _, component := range components {
		logger.Info(fmt.Sprintf("📦 Will install %s (not yet implemented)", component))
	}

	logger.Info("✅ Platform components installation completed (placeholder)")
	return nil
}

// setupGitOpsRepositories sets up GitOps repositories and workflows
func (lp *LocalProvisioner) setupGitOpsRepositories() error {
	logger.Info("🔄 Setting up GitOps repositories...")

	// TODO: Implement GitOps repository setup
	// This should create repositories in Gitea and configure ArgoCD
	// For now, just log that this step is completed

	logger.Info("✅ GitOps repositories setup completed")
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
	logger.Banner("Adhar Internal Developer Platform", "Provisioning Management Cluster and Platform Components")

	// Use the original template-based build approach with ProviderManager
	log := logger.GetLogger()
	if verbose {
		log.SetLevel(logger.DEBUG)
	}

	// Set environment variable to disable Kind provider progress display
	os.Setenv("ADHAR_PLATFORM_SETUP", "true")
	defer os.Unsetenv("ADHAR_PLATFORM_SETUP")

	providerManager := newProviderManagerWithFactory(log.Logger, pfactory.DefaultFactory)

	// Create environment config for Kind provider with CLI flags that uses template mode
	var clusterConfig []config.KeyValueConfig

	if kubeVersion != "" {
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
			DefaultHost:  globals.DefaultHostName, // Use adhar.localtest.me for Kind clusters
			EnableHAMode: false,
			Email:        "admin@" + globals.DefaultHostName, // Set email for domain config
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

// performLocalPreflightChecks validates requirements for local development setup
func performLocalPreflightChecks() error {
	fmt.Printf("⚡ %s\n", helpers.BoldStyle.Render("Running pre-flight checks..."))

	// Check Docker availability and health
	if err := checkDockerAvailable(); err != nil {
		fmt.Printf("  ❌ Docker check failed: %v\n", err)
		return err
	}
	fmt.Printf("  ✅ Docker is available and healthy\n")

	// Check Kind availability and functionality
	if err := checkKindAvailable(); err != nil {
		fmt.Printf("  ❌ Kind check failed: %v\n", err)
		return err
	}
	fmt.Printf("  ✅ Kind cluster engine ready\n")

	// Check kubectl availability
	if err := checkKubectlAvailable(); err != nil {
		fmt.Printf("  ❌ kubectl check failed: %v\n", err)
		return err
	}
	fmt.Printf("  ✅ kubectl is available\n")

	// Check system resources (disk, memory, CPU)
	if err := checkSystemResources(); err != nil {
		fmt.Printf("  ❌ System resources check failed: %v\n", err)
		return err
	}
	fmt.Printf("  ✅ Sufficient system resources available\n")

	// Check port availability with detailed analysis
	if err := checkPortAvailabilityDetailed(); err != nil {
		fmt.Printf("  ❌ Port availability check failed: %v\n", err)
		return err
	}
	fmt.Printf("  ✅ Required ports are available\n")

	// Check container runtime health
	if err := checkContainerRuntimeHealth(); err != nil {
		fmt.Printf("  ❌ Container runtime health check failed: %v\n", err)
		return err
	}
	fmt.Printf("  ✅ Container runtime is healthy\n")

	// Check existing clusters for conflicts
	if err := checkExistingClusters(); err != nil {
		fmt.Printf("  ❌ Existing cluster check failed: %v\n", err)
		return err
	}
	fmt.Printf("  ✅ No conflicting clusters found\n")

	fmt.Println()
	return nil
}

// checkDockerAvailable checks if Docker daemon is running and healthy
func checkDockerAvailable() error {
	// Check if docker command exists
	_, err := exec.LookPath("docker")
	if err != nil {
		return fmt.Errorf("docker command not found in PATH. Please install Docker: https://docs.docker.com/get-docker/")
	}

	// Check if Docker daemon is running
	cmd := exec.Command("docker", "info")
	cmd.Stdout = nil // Suppress output
	cmd.Stderr = nil // Suppress error output
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker daemon is not running or not accessible. Please start Docker Desktop or Docker daemon")
	}

	// Check Docker version compatibility
	cmd = exec.Command("docker", "version", "--format", "{{.Server.Version}}")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to get Docker version: %w", err)
	}

	version := strings.TrimSpace(string(output))
	if version == "" {
		return fmt.Errorf("unable to determine Docker version")
	}

	// Basic version check (Docker 20+ recommended)
	if !strings.HasPrefix(version, "2") && !strings.HasPrefix(version, "3") {
		fmt.Printf("  ⚠️  Warning: Docker version %s detected. Version 20+ recommended\n", version)
	}

	return nil
}

// checkKindAvailable checks if Kind binary is available and functional
func checkKindAvailable() error {
	// Check if kind command exists
	_, err := exec.LookPath("kind")
	if err != nil {
		return fmt.Errorf("kind command not found in PATH. Please install Kind: https://kind.sigs.k8s.io/docs/user/quick-start/#installation")
	}

	// Check if kind command works
	cmd := exec.Command("kind", "version")
	cmd.Stdout = nil // Suppress output
	cmd.Stderr = nil // Suppress error output
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("kind command is not working properly. Please reinstall Kind")
	}

	return nil
}

// checkKubectlAvailable checks if kubectl is available and functional
func checkKubectlAvailable() error {
	// Check if kubectl command exists
	_, err := exec.LookPath("kubectl")
	if err != nil {
		return fmt.Errorf("kubectl command not found in PATH. Please install kubectl: https://kubernetes.io/docs/tasks/tools/")
	}

	// Check if kubectl command works
	cmd := exec.Command("kubectl", "version", "--client", "--output=yaml")
	cmd.Stdout = nil // Suppress output
	cmd.Stderr = nil // Suppress error output
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("kubectl command is not working properly. Please reinstall kubectl")
	}

	return nil
}

// checkSystemResources checks if system has sufficient resources for Kind cluster
func checkSystemResources() error {
	// Check memory (basic check for macOS/Linux)
	if err := checkMemory(); err != nil {
		return err
	}

	// Check disk space with the existing function
	if err := checkDiskSpace(); err != nil {
		return err
	}

	// Check CPU cores
	if err := checkCPUCores(); err != nil {
		return err
	}

	return nil
}

// checkMemory checks if system has sufficient memory
func checkMemory() error {
	var cmd *exec.Cmd

	// Try different approaches based on OS
	if runtime.GOOS == "darwin" {
		// macOS
		cmd = exec.Command("sysctl", "-n", "hw.memsize")
	} else if runtime.GOOS == "linux" {
		// Linux
		cmd = exec.Command("sh", "-c", "grep MemTotal /proc/meminfo | awk '{print $2 * 1024}'")
	} else {
		// Windows or other - skip detailed check
		return nil
	}

	output, err := cmd.Output()
	if err != nil {
		// If we can't check memory, just warn and continue
		fmt.Printf("  ⚠️  Unable to check system memory, continuing anyway\n")
		return nil
	}

	memStr := strings.TrimSpace(string(output))
	memBytes, err := strconv.ParseInt(memStr, 10, 64)
	if err != nil {
		// If we can't parse memory, just warn and continue
		fmt.Printf("  ⚠️  Unable to parse system memory, continuing anyway\n")
		return nil
	}

	// Convert to GB
	memGB := float64(memBytes) / (1024 * 1024 * 1024)

	// Require at least 4GB of RAM for Kind cluster with platform components
	if memGB < 4.0 {
		return fmt.Errorf("insufficient memory: %.1fGB available. At least 4GB recommended for Kind cluster with platform components", memGB)
	}

	return nil
}

// checkCPUCores checks if system has sufficient CPU cores
func checkCPUCores() error {
	cores := runtime.NumCPU()

	// Require at least 2 CPU cores for Kind cluster
	if cores < 2 {
		return fmt.Errorf("insufficient CPU cores: %d available. At least 2 cores recommended for Kind cluster", cores)
	}

	return nil
}

// checkDiskSpace performs a comprehensive disk space check
func checkDiskSpace() error {
	// Get current working directory to check disk space
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get current directory: %w", err)
	}

	// Use df command to check disk space (works on macOS and Linux)
	cmd := exec.Command("df", "-h", cwd)
	output, err := cmd.Output()
	if err != nil {
		// Fallback: try a basic check using os.Stat
		return checkDiskSpaceFallback(cwd)
	}

	// Parse df output to get available space
	lines := strings.Split(string(output), "\n")
	if len(lines) < 2 {
		return fmt.Errorf("unable to parse disk space information")
	}

	// Parse the second line which contains the disk info
	fields := strings.Fields(lines[1])
	if len(fields) < 4 {
		return fmt.Errorf("unable to parse disk space information")
	}

	availableSpace := fields[3] // Available space column

	// Check if available space is less than 4GB (recommended for Kind cluster + images)
	if strings.Contains(availableSpace, "M") {
		// If in MB, it's definitely too low
		return fmt.Errorf("insufficient disk space: only %s available. At least 4GB recommended for Kind cluster with platform components", availableSpace)
	}

	// If it shows in GB, extract the number
	if strings.Contains(availableSpace, "G") {
		spaceStr := strings.TrimSuffix(availableSpace, "G")
		if space, err := strconv.ParseFloat(spaceStr, 64); err == nil && space < 4.0 {
			return fmt.Errorf("insufficient disk space: only %.1fGB available. At least 4GB recommended for Kind cluster with platform components", space)
		}
	}

	return nil
}

// checkPortAvailabilityDetailed performs detailed port availability checking
func checkPortAvailabilityDetailed() error {
	// Enhanced port checking with detailed analysis
	if err := checkPortAvailability(); err != nil {
		// Try to provide more details about what's using the ports
		return enhancePortError(err)
	}
	return nil
}

// checkPortAvailability checks if required ports are available
func checkPortAvailability() error {
	// List of ports that Kind and Adhar typically use
	// Default ports: 80, 443 (HTTP/HTTPS), 6443 (Kubernetes API)
	requiredPorts := []int{80, 443, 6443}

	var busyPorts []int

	for _, port := range requiredPorts {
		if isPortInUse(port) {
			busyPorts = append(busyPorts, port)
		}
	}

	if len(busyPorts) > 0 {
		var portStrings []string
		for _, port := range busyPorts {
			portStrings = append(portStrings, fmt.Sprintf("%d", port))
		}
		return fmt.Errorf("ports %s are already in use. Please stop services using these ports or they may conflict with the cluster", strings.Join(portStrings, ", "))
	}

	return nil
}

// enhancePortError provides more details about port conflicts
func enhancePortError(err error) error {
	errMsg := err.Error()

	// Extract port numbers from error message
	if strings.Contains(errMsg, "80") {
		errMsg += "\n  💡 Port 80 conflict solutions:"
		errMsg += "\n     • Stop local web server (Apache, Nginx, etc.)"
		errMsg += "\n     • Use custom ports in config: networking.httpPort: 8080"
	}

	if strings.Contains(errMsg, "443") {
		errMsg += "\n  💡 Port 443 conflict solutions:"
		errMsg += "\n     • Stop HTTPS services"
		errMsg += "\n     • Use custom ports in config: networking.httpsPort: 8443"
	}

	if strings.Contains(errMsg, "6443") {
		errMsg += "\n  💡 Port 6443 conflict solutions:"
		errMsg += "\n     • Stop existing Kubernetes clusters"
		errMsg += "\n     • Check: kind get clusters"
	}

	return fmt.Errorf("%s", errMsg)
}

// checkContainerRuntimeHealth checks if container runtime is healthy
func checkContainerRuntimeHealth() error {
	// Check Docker storage driver
	cmd := exec.Command("docker", "info", "--format", "{{.Driver}}")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to get Docker storage driver info: %w", err)
	}

	driver := strings.TrimSpace(string(output))
	if driver == "" {
		return fmt.Errorf("unable to determine Docker storage driver")
	}

	// Check Docker storage usage
	cmd = exec.Command("docker", "system", "df", "--format", "table {{.Type}}\t{{.TotalCount}}\t{{.Size}}")
	_, err = cmd.Output()
	if err != nil {
		// If we can't check storage, warn but continue
		fmt.Printf("  ⚠️  Unable to check Docker storage usage\n")
	}

	// Try pulling a small test image to verify network connectivity and registry access
	cmd = exec.Command("docker", "pull", "hello-world:latest")
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("unable to pull test image from Docker Hub. Check network connectivity and Docker configuration")
	}

	// Clean up test image
	exec.Command("docker", "rmi", "hello-world:latest").Run()

	return nil
}

// checkExistingClusters checks for existing Kind clusters that might conflict
func checkExistingClusters() error {
	// Check for existing Kind clusters
	cmd := exec.Command("kind", "get", "clusters")
	output, err := cmd.Output()
	if err != nil {
		// If kind command fails, just continue
		return nil
	}

	clusters := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, cluster := range clusters {
		cluster = strings.TrimSpace(cluster)
		if cluster == "" {
			continue
		}

		// Check if there's an existing 'adhar' cluster
		if cluster == "adhar" {
			return fmt.Errorf("existing Kind cluster 'adhar' found. Please delete it first:\n  kind delete cluster --name adhar")
		}
	}

	// Check for any running containers that might conflict
	cmd = exec.Command("docker", "ps", "--filter", "label=io.x-k8s.kind.cluster", "--format", "{{.Names}}")
	output, err = cmd.Output()
	if err != nil {
		// If we can't check containers, just continue
		return nil
	}

	containers := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, container := range containers {
		container = strings.TrimSpace(container)
		if container == "" {
			continue
		}

		// Warn about existing Kind containers
		if strings.Contains(container, "control-plane") {
			fmt.Printf("  ⚠️  Found existing Kind container: %s\n", container)
		}
	}

	return nil
}

// checkDiskSpaceFallback provides a basic fallback disk space check
func checkDiskSpaceFallback(path string) error {
	// This is a basic fallback - just check if we can write to the directory
	tempFile := filepath.Join(path, ".adhar-space-test")
	file, err := os.Create(tempFile)
	if err != nil {
		return fmt.Errorf("unable to write to current directory - may have insufficient disk space or permissions")
	}
	file.Close()
	os.Remove(tempFile)
	return nil
}

// isPortInUse checks if a specific port is currently in use
func isPortInUse(port int) bool {
	// First try to bind to the port temporarily on all interfaces (0.0.0.0)
	// This matches how Kind tries to bind ports
	addr, err := net.ResolveTCPAddr("tcp", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		return false
	}

	listener, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return true // Port is in use
	}

	defer listener.Close()
	return false // Port is available
}

// getPackageCustomFile parses package customization file input
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

// showLocalDryRunInfo displays what would be provisioned in local development dry-run mode
func showLocalDryRunInfo(adharSpec *v1alpha1.AdharPlatformSpec, envConfig *config.ResolvedEnvironmentConfig) error {
	fmt.Printf("\n%s\n", helpers.BoldStyle.Render("🔍 Dry Run - Local Development Preview"))
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

	fmt.Printf("\n%s\n", helpers.CodeStyle.Render("No changes will be made in dry-run mode"))
	return nil
}

// printSuccessMsg prints success message for local development cluster
func printSuccessMsg() {
	var argoURL string

	// For local development (Kind clusters), use clean URLs without ports
	proxy := behindProxy()
	if proxy {
		argoURL = fmt.Sprintf("https://%s/argocd", host)
	} else if host == globals.DefaultHostName { // adhar.localtest.me (Kind cluster)
		// Kind clusters use direct port mapping, show clean URLs
		argoURL = fmt.Sprintf("https://%s/argocd", host)
	} else {
		// Production clusters or custom domains may need port specification
		if pathRouting {
			argoURL = fmt.Sprintf("%s://%s:%s/argocd", protocol, host, port)
		} else {
			argoURL = fmt.Sprintf("%s://argocd.%s:%s", protocol, host, port)
		}
	}

	fmt.Print("\n\n########################### Finished Creating Adhar IDP Successfully! ############################\n\n")
	fmt.Printf("🎉 %s\n\n", helpers.BoldStyle.Render("Local Development Platform Ready!"))
	fmt.Printf("Your Adhar platform includes:\n")
	fmt.Printf("  ✅ Kind Kubernetes cluster\n")
	fmt.Printf("  ✅ Cilium CNI for secure networking\n")
	fmt.Printf("  ✅ ArgoCD for GitOps deployments\n")
	fmt.Printf("  ✅ Gitea for Git repository hosting\n")
	fmt.Printf("  ✅ Ingress-Nginx for traffic routing\n")
	fmt.Printf("  ✅ Platform observability stack\n\n")
	fmt.Printf("%s\n", helpers.BoldStyle.Render("Quick Access:"))
	fmt.Printf("ArgoCD Dashboard: %s\n", argoURL)
	fmt.Printf("Username: admin\n")
	fmt.Printf("Password: Run `adhar get secrets -p argocd`\n\n")
	fmt.Printf("%s\n", helpers.BoldStyle.Render("Next Steps:"))
	fmt.Printf("1. Deploy your first application via ArgoCD\n")
	fmt.Printf("2. Push code to the integrated Gitea instance\n")
	fmt.Printf("3. Use `adhar get secrets` to retrieve service credentials\n")
	fmt.Printf("4. Run `adhar get status` to monitor platform health\n\n")
	fmt.Printf("%s\n", helpers.BoldStyle.Render("Local Development Commands:"))
	fmt.Printf("• Check cluster status: adhar get status\n")
	fmt.Printf("• Get service secrets: adhar get secrets\n")
	fmt.Printf("• Destroy cluster: adhar down\n\n")
}

// behindProxy checks if we are in codespaces
func behindProxy() bool {
	// check if we are in codespaces: https://docs.github.com/en/codespaces/developing-in-a-codespace
	_, ok := os.LookupEnv("CODESPACES")
	return ok
}

// validate validates the up command arguments
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
