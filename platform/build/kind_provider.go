package build

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
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

	// Create the Kind cluster
	clusterName := envConfig.Name
	kubeVersion := kp.getKubeVersion(envConfig)

	cmd := exec.CommandContext(ctx, "kind", "create", "cluster", "--name", clusterName)
	if kubeVersion != "" {
		cmd.Args = append(cmd.Args, "--image", fmt.Sprintf("kindest/node:%s", kubeVersion))
	}

	kp.logger.Info("Creating Kind cluster", "name", clusterName, "kubeVersion", kubeVersion)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to create Kind cluster: %w, output: %s", err, string(output))
	}

	kp.logger.Info("Kind cluster created successfully", "name", clusterName)
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
	kp.logger.Info("Installing platform services on Kind cluster")

	// Check if helm is available
	if !kp.isHelmInstalled() {
		return fmt.Errorf("helm is not installed. Please install helm from https://helm.sh/docs/intro/install/")
	}

	// Get kubeconfig for the Kind cluster
	kubeconfig, err := kp.GetKubeConfig(ctx, envConfig)
	if err != nil {
		return fmt.Errorf("failed to get kubeconfig: %w", err)
	}

	// Get HA mode setting
	enableHAMode := false
	if envConfig.GlobalSettings != nil {
		enableHAMode = envConfig.GlobalSettings.EnableHAMode
	}

	// Choose installation method: Helm (default) or Templates
	useTemplates := false
	if envConfig.GlobalSettings != nil && envConfig.GlobalSettings.AdharContext == "template-mode" {
		useTemplates = true
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
	} else {
		// Default: Install using Helm directly
		if err := kp.installWithHelm(ctx, kubeconfig, enableHAMode, envConfig); err != nil {
			return fmt.Errorf("failed to install with Helm: %w", err)
		}
	}

	kp.logger.Info("All platform services installed successfully on Kind cluster")
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
	kp.logger.Info("Installing core infrastructure with templates")

	// Core services in installation order (excluding ArgoCD for now)
	coreServices := []string{"cilium", "nginx", "gitea"}

	for _, service := range coreServices {
		kp.logger.Info("Installing core service with templates", "service", service)

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
		if err := kp.waitForServiceReady(ctx, kubeconfig, service); err != nil {
			kp.logger.Warn("Service may not be fully ready", "service", service, "error", err)
		}

		kp.logger.Info("Core service installed successfully", "service", service)
	}

	return nil
}

// setupArgoCDPlatformManagement installs ArgoCD and configures it for platform stack management
func (kp *KindProvider) setupArgoCDPlatformManagement(ctx context.Context, kubeconfig string, enableHAMode bool, envConfig *config.ResolvedEnvironmentConfig) error {
	kp.logger.Info("Setting up ArgoCD for platform management")

	// Install ArgoCD using templates
	kp.logger.Info("Installing ArgoCD with templates")
	manifests, err := kp.templateEngine.GenerateManifests(ctx, "argocd", enableHAMode)
	if err != nil {
		return fmt.Errorf("failed to generate ArgoCD manifests: %w", err)
	}

	if err := kp.applyManifests(ctx, kubeconfig, manifests, "argocd"); err != nil {
		return fmt.Errorf("failed to apply ArgoCD manifests: %w", err)
	}

	// Wait for ArgoCD to be ready
	if err := kp.waitForServiceReady(ctx, kubeconfig, "argocd"); err != nil {
		kp.logger.Warn("ArgoCD may not be fully ready", "error", err)
	}

	// Deploy platform stack applications
	if err := kp.deployPlatformStackApplications(ctx, kubeconfig, envConfig); err != nil {
		return fmt.Errorf("failed to deploy platform stack applications: %w", err)
	}

	kp.logger.Info("ArgoCD platform management setup completed")
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
	kp.logger.Info("Waiting for service to be ready", "service", serviceName)

	// Define service-specific readiness checks
	switch serviceName {
	case "cilium":
		return kp.runKubectlCommand(ctx, kubeconfig, "wait", "--for=condition=ready", "pod", "-l", "app.kubernetes.io/name=cilium-agent", "-n", "kube-system", "--timeout=300s")
	case "nginx":
		return kp.runKubectlCommand(ctx, kubeconfig, "wait", "--for=condition=ready", "pod", "-l", "app.kubernetes.io/name=ingress-nginx", "-n", "ingress-nginx", "--timeout=300s")
	case "gitea":
		return kp.runKubectlCommand(ctx, kubeconfig, "wait", "--for=condition=ready", "pod", "-l", "app.kubernetes.io/name=gitea", "-n", "gitea", "--timeout=300s")
	case "argocd":
		return kp.runKubectlCommand(ctx, kubeconfig, "wait", "--for=condition=ready", "pod", "-l", "app.kubernetes.io/name=argocd-server", "-n", "argocd", "--timeout=300s")
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
	cmdArgs := append([]string{"--kubeconfig", kubeconfig}, args...)
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
	return "~/.kube/config", nil
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
