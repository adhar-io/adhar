package build

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"text/template"
	"time"

	"adhar-io/adhar/platform/config"

	"github.com/sirupsen/logrus"
)

// KindProvider implements Provider for local Kind clusters
type KindProvider struct {
	envConfig      *config.ResolvedEnvironmentConfig
	logger         *logrus.Logger
	templateEngine *TemplateEngine
}

// NewKindProvider creates a new Kind provider
func NewKindProvider(envConfig *config.ResolvedEnvironmentConfig, logger *logrus.Logger, templateEngine *TemplateEngine) (Provider, error) {
	return &KindProvider{
		envConfig:      envConfig,
		logger:         logger,
		templateEngine: templateEngine,
	}, nil
}

// Provision creates a new Kind cluster
func (kp *KindProvider) Provision(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig, opts ProvisionOptions) error {
	if opts.DryRun {
		kp.logger.Info("DRY-RUN: Would provision Kind cluster", "name", envConfig.Name)
		return nil
	}

	kp.logger.Info("Provisioning Kind cluster", "name", envConfig.Name)

	// Check if kind is installed
	if !kp.isKindInstalled() {
		return fmt.Errorf("kind is not installed. Please install kind from https://kind.sigs.k8s.io/docs/user/quick-start/")
	}

	// Check if cluster already exists
	exists, err := kp.Exists(ctx, envConfig)
	if err != nil {
		return fmt.Errorf("failed to check if Kind cluster exists: %w", err)
	}

	if exists {
		kp.logger.Info("Kind cluster already exists", "name", envConfig.Name)
		return nil
	}

	// Check if we should use template mode
	useTemplates := false
	if envConfig.GlobalSettings != nil && envConfig.GlobalSettings.AdharContext == "template-mode" {
		useTemplates = true
	}

	clusterName := envConfig.Name
	kubeVersion := kp.getKubeVersion(envConfig)

	if useTemplates {
		// Use template-based cluster creation (original approach)
		kp.logger.Info("🏗️  Creating Kind cluster using template configuration...")
		kp.logger.Info("   Cluster name: " + clusterName)
		kp.logger.Info("   Kubernetes version: " + kubeVersion)
		kp.logger.Info("   Configuration: 1 control-plane + 3 worker nodes")
		kp.logger.Info("   Networking: CNI disabled (Cilium will be installed)")
		if err := kp.createClusterWithTemplate(ctx, clusterName, kubeVersion, envConfig); err != nil {
			return fmt.Errorf("failed to create Kind cluster with template: %w", err)
		}
	} else {
		// Use basic cluster creation
		kp.logger.Info("Creating Kind cluster using basic mode", "name", clusterName, "kubeVersion", kubeVersion)
		cmd := exec.CommandContext(ctx, "kind", "create", "cluster", "--name", clusterName)
		if kubeVersion != "" {
			cmd.Args = append(cmd.Args, "--image", fmt.Sprintf("kindest/node:%s", kubeVersion))
		}

		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("failed to create Kind cluster: %w, output: %s", err, string(output))
		}
	}

	kp.logger.Info("✅ Kind cluster created successfully: " + clusterName)
	return nil
}

// Destroy destroys a Kind cluster
func (kp *KindProvider) Destroy(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig, opts ProvisionOptions) error {
	if opts.DryRun {
		kp.logger.Info("DRY-RUN: Would destroy Kind cluster", "name", envConfig.Name)
		return nil
	}

	kp.logger.Info("Destroying Kind cluster", "name", envConfig.Name)

	clusterName := envConfig.Name
	cmd := exec.CommandContext(ctx, "kind", "delete", "cluster", "--name", clusterName)

	output, err := cmd.CombinedOutput()
	if err != nil {
		// Kind delete can fail if cluster doesn't exist, which is fine
		if strings.Contains(string(output), "not found") {
			kp.logger.Info("Kind cluster not found, nothing to destroy", "name", clusterName)
			return nil
		}
		return fmt.Errorf("failed to destroy Kind cluster: %w, output: %s", err, string(output))
	}

	kp.logger.Info("Kind cluster destroyed successfully", "name", clusterName)
	return nil
}

// Exists checks if a Kind cluster exists
func (kp *KindProvider) Exists(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig) (bool, error) {
	cmd := exec.CommandContext(ctx, "kind", "get", "clusters")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("failed to list Kind clusters: %w", err)
	}

	clusters := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, cluster := range clusters {
		if strings.TrimSpace(cluster) == envConfig.Name {
			return true, nil
		}
	}

	return false, nil
}

// InstallPlatformServices installs platform services on the Kind cluster
func (kp *KindProvider) InstallPlatformServices(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig) error {
	kp.logger.Info("🚀 Installing platform services on Kind cluster...")
	kp.logger.Info("   This will install: Cilium, Nginx, Gitea, and ArgoCD")

	// Check if helm is available
	if !kp.isHelmInstalled() {
		return fmt.Errorf("helm is not installed. Please install helm from https://helm.sh/docs/intro/install/")
	}

	// Get kubeconfig for the Kind cluster
	kubeconfig, err := kp.GetKubeConfig(ctx, envConfig)
	if err != nil {
		return fmt.Errorf("failed to get kubeconfig: %w", err)
	}

	// Force local development mode (non-HA, minimal replicas) for Kind clusters
	enableHAMode := false // Always force local mode for Kind clusters
	kp.logger.Info("   Enforcing local development mode (minimal replicas)")

	// Choose installation method: Helm (default) or Templates
	useTemplates := false
	if envConfig.GlobalSettings != nil && envConfig.GlobalSettings.AdharContext == "template-mode" {
		useTemplates = true
		kp.logger.Info("   Using Kustomize-based templates with local development overlays")
	}

	if useTemplates {
		// Phase 1: Install core infrastructure using templates (like cloud providers)
		if err := kp.installCoreInfrastructureWithTemplates(ctx, kubeconfig, enableHAMode, envConfig); err != nil {
			return fmt.Errorf("failed to install core infrastructure with templates: %w", err)
		}

		// Phase 2: Setup ArgoCD for platform stack management
		if err := kp.setupArgoCDPlatformManagement(ctx, kubeconfig, enableHAMode, envConfig); err != nil {
			return fmt.Errorf("failed to setup ArgoCD platform management: %w", err)
		}

		// Final verification and success message
		kp.logger.Info("🔍 Running final platform verification...")
		if err := kp.verifyPlatformServices(ctx, kubeconfig); err != nil {
			kp.logger.Warn("⚠️  Some services may not be fully ready yet", "error", err)
		}

		kp.logger.Info("🎉✨ Adhar platform is ready! ✨🎉")
		kp.logger.Info("🌐 Access your services at:")
		kp.printServiceURLs(envConfig)
	} else {
		// Default: Install using Helm directly
		if err := kp.installWithHelm(ctx, kubeconfig, enableHAMode, envConfig); err != nil {
			return fmt.Errorf("failed to install with Helm: %w", err)
		}
		kp.logger.Info("All platform services installed successfully on Kind cluster")
	}
	return nil
}

// installWithHelm installs platform services using Helm (existing implementation)
func (kp *KindProvider) installWithHelm(ctx context.Context, kubeconfig string, enableHAMode bool, envConfig *config.ResolvedEnvironmentConfig) error {
	// Define the order of services to install
	services := []string{"cilium", "nginx", "gitea", "argocd"}

	for _, service := range services {
		kp.logger.Info("Installing platform service", "service", service)

		if err := kp.installService(ctx, service, kubeconfig, enableHAMode, envConfig); err != nil {
			return fmt.Errorf("failed to install service %s: %w", service, err)
		}

		kp.logger.Info("Platform service installed successfully", "service", service)
	}

	return nil
}

// installCoreInfrastructureWithTemplates installs core infrastructure using templates (like cloud providers)
func (kp *KindProvider) installCoreInfrastructureWithTemplates(ctx context.Context, kubeconfig string, enableHAMode bool, envConfig *config.ResolvedEnvironmentConfig) error {
	kp.logger.Info("🚀 Installing core infrastructure with templates...")

	// Step 1: Create the adhar-system namespace first (required for all platform services)
	kp.logger.Info("📁 Creating adhar-system namespace...")
	if err := kp.createAdharSystemNamespace(ctx, kubeconfig); err != nil {
		return fmt.Errorf("failed to create adhar-system namespace: %w", err)
	}
	kp.logger.Info("✅ adhar-system namespace created successfully")

	// Step 2: Install Cilium first (CNI must be ready before other services)
	kp.logger.Info("🔗 Installing Cilium CNI (Container Network Interface)...")
	kp.logger.Info("   This may take a few minutes as images are pulled...")

	ciliumManifests, err := kp.templateEngine.GenerateManifests(ctx, "cilium", enableHAMode)
	if err != nil {
		return fmt.Errorf("failed to generate Cilium manifests: %w", err)
	}

	if err := kp.applyManifests(ctx, kubeconfig, ciliumManifests, "cilium"); err != nil {
		return fmt.Errorf("failed to apply Cilium manifests: %w", err)
	}

	// Wait for Cilium to be ready - this is critical for cluster networking
	kp.logger.Info("⏳ Waiting for Cilium to be ready (this enables cluster networking)...")
	if err := kp.waitForCiliumReady(ctx, kubeconfig); err != nil {
		return fmt.Errorf("Cilium failed to become ready: %w", err)
	}
	kp.logger.Info("✅ Cilium CNI is ready - cluster networking is now active")

	// Verify nodes are ready after CNI is installed
	kp.logger.Info("🔍 Verifying cluster nodes are ready...")
	if err := kp.waitForNodesReady(ctx, kubeconfig); err != nil {
		kp.logger.Warn("Some nodes may not be ready yet, but continuing...", "error", err)
	} else {
		kp.logger.Info("✅ All cluster nodes are ready")
	}

	// Step 3: Install other core services (in order)
	otherServices := []string{"nginx", "gitea"}

	for _, service := range otherServices {
		kp.logger.Info("🔧 Installing platform service: " + service + "...")

		// Generate manifests using the template engine
		manifests, err := kp.templateEngine.GenerateManifests(ctx, service, enableHAMode)
		if err != nil {
			return fmt.Errorf("failed to generate manifests for %s: %w", service, err)
		}

		// Apply manifests using kubectl
		if err := kp.applyManifests(ctx, kubeconfig, manifests, service); err != nil {
			return fmt.Errorf("failed to apply manifests for %s: %w", service, err)
		}

		// Wait for service to be ready
		kp.logger.Info("⏳ Waiting for " + service + " to be ready...")
		if err := kp.waitForServiceReady(ctx, kubeconfig, service); err != nil {
			kp.logger.Warn("⚠️  "+service+" may not be fully ready yet, but continuing...", "error", err)
		} else {
			kp.logger.Info("✅ " + service + " is ready")
		}
	}

	kp.logger.Info("🎉 Core infrastructure installation completed successfully")
	return nil
}

// setupArgoCDPlatformManagement installs ArgoCD and configures it for platform stack management
func (kp *KindProvider) setupArgoCDPlatformManagement(ctx context.Context, kubeconfig string, enableHAMode bool, envConfig *config.ResolvedEnvironmentConfig) error {
	kp.logger.Info("🔄 Setting up ArgoCD for platform management...")

	// Install ArgoCD using templates
	kp.logger.Info("🔧 Installing ArgoCD...")
	manifests, err := kp.templateEngine.GenerateManifests(ctx, "argocd", enableHAMode)
	if err != nil {
		return fmt.Errorf("failed to generate ArgoCD manifests: %w", err)
	}

	if err := kp.applyManifests(ctx, kubeconfig, manifests, "argocd"); err != nil {
		return fmt.Errorf("failed to apply ArgoCD manifests: %w", err)
	}

	// Wait for ArgoCD to be ready
	kp.logger.Info("⏳ Waiting for ArgoCD to be ready...")
	if err := kp.waitForServiceReady(ctx, kubeconfig, "argocd"); err != nil {
		kp.logger.Warn("⚠️  ArgoCD may not be fully ready yet, but continuing...", "error", err)
	} else {
		kp.logger.Info("✅ ArgoCD is ready")
	}

	// Deploy platform stack applications
	kp.logger.Info("📦 Deploying platform stack applications...")
	if err := kp.deployPlatformStackApplications(ctx, kubeconfig, envConfig); err != nil {
		kp.logger.Warn("⚠️  Some platform stack applications may not have deployed successfully", "error", err)
	} else {
		kp.logger.Info("✅ Platform stack applications deployed")
	}

	kp.logger.Info("🎉 ArgoCD platform management setup completed")
	return nil
}

// deployPlatformStackApplications deploys the platform stack application sets to ArgoCD
func (kp *KindProvider) deployPlatformStackApplications(ctx context.Context, kubeconfig string, envConfig *config.ResolvedEnvironmentConfig) error {
	kp.logger.Info("Deploying platform stack applications via ArgoCD")

	// Define the platform stack applications to deploy
	platformApps := []string{
		"platform/stack/adhar-appset-charts.yaml",
		"platform/stack/adhar-appset-manifests.yaml",
		"platform/stack/adhar-templates.yaml",
	}

	for _, appPath := range platformApps {
		kp.logger.Info("Deploying platform application", "app", appPath)

		if err := kp.runKubectlCommand(ctx, kubeconfig, "apply", "-f", appPath); err != nil {
			kp.logger.Warn("Failed to deploy platform application", "app", appPath, "error", err)
			// Continue with other applications even if one fails
			continue
		}

		kp.logger.Info("Platform application deployed", "app", appPath)
	}

	return nil
}

// applyManifests applies Kubernetes manifests using kubectl
func (kp *KindProvider) applyManifests(ctx context.Context, kubeconfig, manifests, serviceName string) error {
	kp.logger.Info("Applying manifests", "service", serviceName)

	// Create a temporary file for the manifests
	tmpFile := fmt.Sprintf("/tmp/%s-%s-manifests.yaml", serviceName, "temp")

	if err := os.WriteFile(tmpFile, []byte(manifests), 0644); err != nil {
		return fmt.Errorf("failed to write manifests to file: %w", err)
	}
	defer os.Remove(tmpFile)

	// Apply using kubectl
	if err := kp.runKubectlCommand(ctx, kubeconfig, "apply", "-f", tmpFile); err != nil {
		return fmt.Errorf("failed to apply manifests: %w", err)
	}

	return nil
}

// waitForServiceReady waits for a service to be ready
func (kp *KindProvider) waitForServiceReady(ctx context.Context, kubeconfig, serviceName string) error {
	// Define service-specific readiness checks (all services are in adhar-system namespace)
	switch serviceName {
	case "cilium":
		// Cilium uses a more comprehensive check in waitForCiliumReady with extended timeout
		return kp.runKubectlCommand(ctx, kubeconfig, "wait", "--for=condition=ready", "pod", "-l", "k8s-app=cilium", "-n", "adhar-system", "--timeout=600s")
	case "nginx":
		return kp.runKubectlCommand(ctx, kubeconfig, "wait", "--for=condition=ready", "pod", "-l", "app.kubernetes.io/name=ingress-nginx", "-n", "adhar-system", "--timeout=300s")
	case "gitea":
		return kp.runKubectlCommand(ctx, kubeconfig, "wait", "--for=condition=ready", "pod", "-l", "app.kubernetes.io/name=gitea", "-n", "adhar-system", "--timeout=300s")
	case "argocd":
		return kp.runKubectlCommand(ctx, kubeconfig, "wait", "--for=condition=ready", "pod", "-l", "app.kubernetes.io/name=argocd-server", "-n", "adhar-system", "--timeout=300s")
	default:
		// Generic wait - just give it some time
		time.Sleep(30 * time.Second)
		return nil
	}
}

// installService installs a specific platform service using Helm
func (kp *KindProvider) installService(ctx context.Context, serviceName, kubeconfig string, enableHAMode bool, envConfig *config.ResolvedEnvironmentConfig) error {
	switch serviceName {
	case "cilium":
		return kp.installCilium(ctx, kubeconfig)
	case "nginx":
		return kp.installNginx(ctx, kubeconfig, envConfig)
	case "gitea":
		return kp.installGitea(ctx, kubeconfig, enableHAMode, envConfig)
	case "argocd":
		return kp.installArgoCD(ctx, kubeconfig, enableHAMode, envConfig)
	default:
		return fmt.Errorf("unknown service: %s", serviceName)
	}
}

// installCilium installs Cilium CNI using Helm
func (kp *KindProvider) installCilium(ctx context.Context, kubeconfig string) error {
	kp.logger.Info("Installing Cilium CNI")

	// Add Cilium Helm repository
	if err := kp.runHelmCommand(ctx, kubeconfig, "repo", "add", "cilium", "https://helm.cilium.io/"); err != nil {
		return fmt.Errorf("failed to add Cilium repo: %w", err)
	}

	if err := kp.runHelmCommand(ctx, kubeconfig, "repo", "update"); err != nil {
		return fmt.Errorf("failed to update Helm repos: %w", err)
	}

	// Install Cilium
	args := []string{
		"install", "cilium", "cilium/cilium",
		"--namespace", "kube-system",
		"--set", "operator.replicas=1",
		"--set", "hubble.relay.enabled=true",
		"--set", "hubble.ui.enabled=true",
	}

	if err := kp.runHelmCommand(ctx, kubeconfig, args...); err != nil {
		return fmt.Errorf("failed to install Cilium: %w", err)
	}

	return nil
}

// installNginx installs NGINX Ingress Controller using Helm
func (kp *KindProvider) installNginx(ctx context.Context, kubeconfig string, envConfig *config.ResolvedEnvironmentConfig) error {
	kp.logger.Info("Installing NGINX Ingress Controller")

	// Add NGINX Helm repository
	if err := kp.runHelmCommand(ctx, kubeconfig, "repo", "add", "ingress-nginx", "https://kubernetes.github.io/ingress-nginx"); err != nil {
		return fmt.Errorf("failed to add NGINX repo: %w", err)
	}

	if err := kp.runHelmCommand(ctx, kubeconfig, "repo", "update"); err != nil {
		return fmt.Errorf("failed to update Helm repos: %w", err)
	}

	// Create namespace
	if err := kp.runKubectlCommand(ctx, kubeconfig, "create", "namespace", "ingress-nginx", "--dry-run=client", "-o", "yaml"); err != nil {
		kp.logger.Debug("Namespace creation command prepared")
	}

	if err := kp.runKubectlCommand(ctx, kubeconfig, "apply", "-f", "-"); err != nil {
		kp.logger.Debug("Failed to create namespace, may already exist")
	}

	// Install NGINX Ingress Controller
	args := []string{
		"install", "ingress-nginx", "ingress-nginx/ingress-nginx",
		"--namespace", "ingress-nginx",
		"--set", "controller.service.type=NodePort",
		"--set", "controller.hostPort.enabled=true",
		"--set", "controller.service.nodePorts.http=30080",
		"--set", "controller.service.nodePorts.https=30443",
	}

	if err := kp.runHelmCommand(ctx, kubeconfig, args...); err != nil {
		return fmt.Errorf("failed to install NGINX: %w", err)
	}

	return nil
}

// installGitea installs Gitea using Helm
func (kp *KindProvider) installGitea(ctx context.Context, kubeconfig string, enableHAMode bool, envConfig *config.ResolvedEnvironmentConfig) error {
	kp.logger.Info("Installing Gitea")

	// Add Gitea Helm repository
	if err := kp.runHelmCommand(ctx, kubeconfig, "repo", "add", "gitea-charts", "https://dl.gitea.io/charts/"); err != nil {
		return fmt.Errorf("failed to add Gitea repo: %w", err)
	}

	if err := kp.runHelmCommand(ctx, kubeconfig, "repo", "update"); err != nil {
		return fmt.Errorf("failed to update Helm repos: %w", err)
	}

	// Create namespace
	if err := kp.runKubectlCommand(ctx, kubeconfig, "create", "namespace", "gitea"); err != nil {
		kp.logger.Debug("Failed to create namespace, may already exist")
	}

	// Install Gitea
	args := []string{
		"install", "gitea", "gitea-charts/gitea",
		"--namespace", "gitea",
		"--set", "gitea.admin.username=adhar",
		"--set", "gitea.admin.password=developer",
		"--set", "gitea.admin.email=admin@adhar.local",
		"--set", "postgresql.enabled=true",
		"--set", "redis.enabled=true",
	}

	if !enableHAMode {
		args = append(args, "--set", "replicaCount=1")
	}

	if err := kp.runHelmCommand(ctx, kubeconfig, args...); err != nil {
		return fmt.Errorf("failed to install Gitea: %w", err)
	}

	return nil
}

// installArgoCD installs ArgoCD using Helm
func (kp *KindProvider) installArgoCD(ctx context.Context, kubeconfig string, enableHAMode bool, envConfig *config.ResolvedEnvironmentConfig) error {
	kp.logger.Info("Installing ArgoCD")

	// Add ArgoCD Helm repository
	if err := kp.runHelmCommand(ctx, kubeconfig, "repo", "add", "argo", "https://argoproj.github.io/argo-helm"); err != nil {
		return fmt.Errorf("failed to add ArgoCD repo: %w", err)
	}

	if err := kp.runHelmCommand(ctx, kubeconfig, "repo", "update"); err != nil {
		return fmt.Errorf("failed to update Helm repos: %w", err)
	}

	// Create namespace
	if err := kp.runKubectlCommand(ctx, kubeconfig, "create", "namespace", "argocd"); err != nil {
		kp.logger.Debug("Failed to create namespace, may already exist")
	}

	// Install ArgoCD
	args := []string{
		"install", "argocd", "argo/argo-cd",
		"--namespace", "argocd",
		"--set", "server.service.type=NodePort",
		"--set", "configs.secret.argocdServerAdminPassword=$2a$10$mzMOLp.tUTUyKN.HwFEr6.vCnR2hCBMVNzrLwREGGq.LWQrZO2C2a", // password: developer
	}

	if !enableHAMode {
		args = append(args, "--set", "server.replicas=1")
		args = append(args, "--set", "controller.replicas=1")
		args = append(args, "--set", "repoServer.replicas=1")
	}

	if err := kp.runHelmCommand(ctx, kubeconfig, args...); err != nil {
		return fmt.Errorf("failed to install ArgoCD: %w", err)
	}

	return nil
}

// runHelmCommand runs a helm command with the specified kubeconfig
func (kp *KindProvider) runHelmCommand(ctx context.Context, kubeconfig string, args ...string) error {
	cmdArgs := append([]string{"--kubeconfig", kubeconfig}, args...)
	cmd := exec.CommandContext(ctx, "helm", cmdArgs...)

	output, err := cmd.CombinedOutput()
	if err != nil {
		kp.logger.Error("Helm command failed", "cmd", cmd.String(), "output", string(output), "error", err)
		return fmt.Errorf("helm command failed: %w", err)
	}

	kp.logger.Debug("Helm command succeeded", "cmd", cmd.String())
	return nil
}

// runKubectlCommand runs a kubectl command with the specified kubeconfig
func (kp *KindProvider) runKubectlCommand(ctx context.Context, kubeconfig string, args ...string) error {
	var cmdArgs []string
	if kubeconfig != "" {
		cmdArgs = append([]string{"--kubeconfig", kubeconfig}, args...)
	} else {
		cmdArgs = args
	}
	cmd := exec.CommandContext(ctx, "kubectl", cmdArgs...)

	output, err := cmd.CombinedOutput()
	if err != nil {
		kp.logger.Debug("kubectl command failed", "cmd", cmd.String(), "output", string(output), "error", err)
		return fmt.Errorf("kubectl command failed: %w", err)
	}

	kp.logger.Debug("kubectl command succeeded", "cmd", cmd.String())
	return nil
}

// isHelmInstalled checks if Helm is installed and available
func (kp *KindProvider) isHelmInstalled() bool {
	cmd := exec.Command("helm", "version", "--short")
	err := cmd.Run()
	return err == nil
}

// ValidateCluster validates the Kind cluster
func (kp *KindProvider) ValidateCluster(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig) error {
	kp.logger.Info("Validating Kind cluster")
	// TODO: Implement Kind cluster validation
	return nil
}

// GetClusterInfo returns information about the Kind cluster
func (kp *KindProvider) GetClusterInfo(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig) (*ClusterInfo, error) {
	return &ClusterInfo{
		Name:      envConfig.Name,
		Provider:  "kind",
		Region:    "local",
		Status:    "unknown",
		NodeCount: 1,
		Version:   "v1.28.0",
		Endpoint:  "https://127.0.0.1:6443",
		Metadata: map[string]string{
			"type":     "local",
			"provider": "kind",
		},
	}, nil
}

// GetKubeConfig returns the kubeconfig for the Kind cluster
func (kp *KindProvider) GetKubeConfig(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig) (string, error) {
	// For Kind clusters, we need to export the kubeconfig from the cluster
	// This returns the path to a temporary kubeconfig file specific to this Kind cluster
	clusterName := envConfig.Name

	// Export kubeconfig from Kind cluster
	cmd := exec.CommandContext(ctx, "kind", "export", "kubeconfig", "--name", clusterName)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to export kubeconfig for Kind cluster %s: %w, output: %s", clusterName, err, string(output))
	}

	// Kind exports to the default kubeconfig location, so we return that path
	// But we need to make sure we're using the right context
	return "", nil // We'll use kubectl without --kubeconfig, relying on the current context
}

// Helper methods

// isKindInstalled checks if kind is installed and available
func (kp *KindProvider) isKindInstalled() bool {
	cmd := exec.Command("kind", "version")
	err := cmd.Run()
	return err == nil
}

// getKubeVersion extracts Kubernetes version from cluster configuration
func (kp *KindProvider) getKubeVersion(envConfig *config.ResolvedEnvironmentConfig) string {
	for _, cfg := range envConfig.ResolvedClusterConfig {
		if cfg.Key == "kubeVersion" {
			return cfg.Value
		}
	}
	return "v1.33.1" // Default version
}

// createAdharSystemNamespace creates the adhar-system namespace required by platform services
func (kp *KindProvider) createAdharSystemNamespace(ctx context.Context, kubeconfig string) error {
	kp.logger.Info("Creating adhar-system namespace")

	// Read the namespace manifest from template file
	templatePath := "platform/build/templates/k8s/adhar-system-namespace.yaml"
	manifestBytes, err := os.ReadFile(templatePath)
	if err != nil {
		return fmt.Errorf("failed to read namespace template file %s: %w", templatePath, err)
	}
	namespaceManifest := string(manifestBytes)

	// Apply the namespace manifest using kubectl
	var cmd *exec.Cmd
	if kubeconfig != "" {
		cmd = exec.CommandContext(ctx, "kubectl", "--kubeconfig", kubeconfig, "apply", "-f", "-")
	} else {
		cmd = exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
	}
	cmd.Stdin = strings.NewReader(namespaceManifest)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to create adhar-system namespace: %w, output: %s", err, string(output))
	}

	kp.logger.Info("adhar-system namespace created successfully")
	return nil
}

// createClusterWithTemplate creates a Kind cluster using the kind.yaml.tmpl template file
func (kp *KindProvider) createClusterWithTemplate(ctx context.Context, clusterName, kubeVersion string, envConfig *config.ResolvedEnvironmentConfig) error {
	// Read the Kind cluster template from file
	templatePath := "platform/build/templates/kind/kind.yaml.tmpl"
	templateBytes, err := os.ReadFile(templatePath)
	if err != nil {
		return fmt.Errorf("failed to read Kind template file %s: %w", templatePath, err)
	}
	kindTemplate := string(templateBytes)

	// Prepare template data with all required variables for the template
	templateData := map[string]interface{}{
		"KubernetesVersion": kubeVersion,
		"Protocol":          "https",
		"Port":              "8443",
		"Host":              "adhar.localtest.me",
		"UsePathRouting":    false,
		"StaticPassword":    false,
		"ExtraPortsMapping": []map[string]interface{}{},
		"RegistryConfig":    "",
	}

	// Override with any configuration from envConfig if available
	for _, cfg := range envConfig.ResolvedClusterConfig {
		switch cfg.Key {
		case "protocol":
			templateData["Protocol"] = cfg.Value
		case "port":
			templateData["Port"] = cfg.Value
		case "host":
			templateData["Host"] = cfg.Value
		case "usePathRouting":
			templateData["UsePathRouting"] = cfg.Value == "true"
		case "staticPassword":
			templateData["StaticPassword"] = cfg.Value == "true"
		}
	}

	// Render the template
	renderedConfig, err := kp.renderKindTemplate(kindTemplate, templateData)
	if err != nil {
		return fmt.Errorf("failed to render Kind template: %w", err)
	}

	kp.logger.Info("########################### Adhar kind config ############################")
	kp.logger.Info("\n" + renderedConfig)
	kp.logger.Info("#########################   config end    ############################")

	// Create the cluster using the rendered config
	cmd := exec.CommandContext(ctx, "kind", "create", "cluster", "--name", clusterName, "--config", "-")
	cmd.Stdin = strings.NewReader(renderedConfig)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to create Kind cluster with template: %w, output: %s", err, string(output))
	}

	kp.logger.Info("Kind cluster created successfully with template", "name", clusterName)
	return nil
}

// renderKindTemplate renders the Kind template with the provided data
func (kp *KindProvider) renderKindTemplate(templateStr string, data map[string]interface{}) (string, error) {
	tmpl, err := template.New("kind").Parse(templateStr)
	if err != nil {
		return "", fmt.Errorf("failed to parse Kind template: %w", err)
	}

	var rendered strings.Builder
	if err := tmpl.Execute(&rendered, data); err != nil {
		return "", fmt.Errorf("failed to execute Kind template: %w", err)
	}

	return rendered.String(), nil
}

// waitForCiliumReady waits specifically for Cilium to be ready with proper logging and extended timeouts
func (kp *KindProvider) waitForCiliumReady(ctx context.Context, kubeconfig string) error {
	kp.logger.Info("🔍 Starting comprehensive Cilium readiness check...")

	// Wait for Cilium operator to be ready first with extended timeout
	kp.logger.Info("   Phase 1: Waiting for Cilium operator deployment...")
	if err := kp.runKubectlCommand(ctx, kubeconfig, "wait", "--for=condition=available", "deployment", "cilium-operator", "-n", "adhar-system", "--timeout=600s"); err != nil {
		kp.logger.Error("Cilium operator deployment failed to become ready, checking status...")
		kp.runCiliumDiagnostics(ctx, kubeconfig)
		return fmt.Errorf("Cilium operator deployment failed to become ready: %w", err)
	}
	kp.logger.Info("   ✅ Cilium operator deployment is ready")

	// Wait for Cilium DaemonSet to be ready with extended timeout (10 minutes)
	kp.logger.Info("   Phase 2: Waiting for Cilium DaemonSet pods (this may take several minutes)...")
	if err := kp.runKubectlCommand(ctx, kubeconfig, "wait", "--for=condition=ready", "pod", "-l", "k8s-app=cilium", "-n", "adhar-system", "--timeout=600s"); err != nil {
		kp.logger.Error("Cilium DaemonSet pods failed to become ready, running diagnostics...")
		kp.runCiliumDiagnostics(ctx, kubeconfig)
		return fmt.Errorf("Cilium DaemonSet pods failed to become ready: %w", err)
	}
	kp.logger.Info("   ✅ Cilium DaemonSet pods are ready")

	// Verify that Cilium is providing networking
	kp.logger.Info("   Phase 3: Verifying Cilium networking...")
	if err := kp.runKubectlCommand(ctx, kubeconfig, "get", "ciliumnodes", "--timeout=60s"); err != nil {
		kp.logger.Debug("CiliumNodes CRD not yet available, this is normal during initial startup")
	} else {
		kp.logger.Info("   ✅ Cilium networking CRDs are available")
	}

	kp.logger.Info("🎉 Cilium is fully ready and operational!")
	return nil
}

// waitForNodesReady waits for all cluster nodes to be in Ready state
func (kp *KindProvider) waitForNodesReady(ctx context.Context, kubeconfig string) error {
	return kp.runKubectlCommand(ctx, kubeconfig, "wait", "--for=condition=ready", "nodes", "--all", "--timeout=120s")
}

// verifyPlatformServices performs final verification of all platform services
func (kp *KindProvider) verifyPlatformServices(ctx context.Context, kubeconfig string) error {
	services := []struct {
		name      string
		namespace string
		selector  string
	}{
		{"cilium", "adhar-system", "k8s-app=cilium"},
		{"nginx", "adhar-system", "app.kubernetes.io/name=ingress-nginx"},
		{"gitea", "adhar-system", "app.kubernetes.io/name=gitea"},
		{"argocd", "adhar-system", "app.kubernetes.io/name=argocd-server"},
	}

	allReady := true
	for _, svc := range services {
		kp.logger.Info("   Checking " + svc.name + "...")
		if err := kp.runKubectlCommand(ctx, kubeconfig, "get", "pods", "-l", svc.selector, "-n", svc.namespace, "--no-headers"); err != nil {
			kp.logger.Warn("   ⚠️  " + svc.name + " pods not found")
			allReady = false
			continue
		}
		kp.logger.Info("   ✅ " + svc.name + " pods found")
	}

	if !allReady {
		return fmt.Errorf("some services are not fully ready")
	}

	return nil
}

// printServiceURLs prints the URLs where users can access the platform services
func (kp *KindProvider) printServiceURLs(envConfig *config.ResolvedEnvironmentConfig) {
	host := "adhar.localtest.me"
	port := "8443"
	protocol := "https"

	// Try to get host and port from config if available
	for _, cfg := range envConfig.ResolvedClusterConfig {
		switch cfg.Key {
		case "host":
			host = cfg.Value
		case "port":
			port = cfg.Value
		case "protocol":
			protocol = cfg.Value
		}
	}

	baseURL := fmt.Sprintf("%s://%s:%s", protocol, host, port)

	kp.logger.Info("   🎯 ArgoCD: " + baseURL + "/argocd")
	kp.logger.Info("   🐙 Gitea: " + baseURL + "/gitea")
	kp.logger.Info("   🌐 Nginx: " + baseURL)
	kp.logger.Info("")
	kp.logger.Info("💡 Default credentials:")
	kp.logger.Info("   ArgoCD: admin / developer")
	kp.logger.Info("   Gitea: adhar / developer")
}

// runCiliumDiagnostics runs diagnostic commands to help troubleshoot Cilium issues
func (kp *KindProvider) runCiliumDiagnostics(ctx context.Context, kubeconfig string) {
	kp.logger.Info("🔧 Running Cilium diagnostics for troubleshooting...")

	// Check Cilium pod status
	kp.logger.Info("   Checking Cilium pod status...")
	if err := kp.runKubectlCommand(ctx, kubeconfig, "get", "pods", "-n", "adhar-system", "-l", "k8s-app=cilium", "-o", "wide"); err != nil {
		kp.logger.Warn("Failed to get Cilium pod status", "error", err)
	}

	// Check Cilium operator status
	kp.logger.Info("   Checking Cilium operator status...")
	if err := kp.runKubectlCommand(ctx, kubeconfig, "get", "deployment", "-n", "adhar-system", "cilium-operator", "-o", "wide"); err != nil {
		kp.logger.Warn("Failed to get Cilium operator status", "error", err)
	}

	// Check events for Cilium-related issues
	kp.logger.Info("   Checking recent events for Cilium issues...")
	if err := kp.runKubectlCommand(ctx, kubeconfig, "get", "events", "-n", "adhar-system", "--sort-by=.lastTimestamp"); err != nil {
		kp.logger.Warn("Failed to get Cilium events", "error", err)
	}

	// Get logs from Cilium pods (if they exist)
	kp.logger.Info("   Attempting to get Cilium pod logs...")
	if err := kp.runKubectlCommand(ctx, kubeconfig, "logs", "-n", "adhar-system", "-l", "k8s-app=cilium", "--tail=20", "--prefix=true"); err != nil {
		kp.logger.Debug("Could not retrieve Cilium pod logs (pods may not be running)", "error", err)
	}
}
