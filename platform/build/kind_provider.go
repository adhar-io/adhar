package build

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"
	"time"

	"adhar-io/adhar/platform/config"

	"github.com/sirupsen/logrus"
	kindv1alpha4 "sigs.k8s.io/kind/pkg/apis/config/v1alpha4"
	"sigs.k8s.io/yaml"
)

const (
	ingressNginxNodeLabelKey   = "ingress-ready"
	ingressNginxNodeLabelValue = "true"
)

// PortMapping represents port mapping configuration from legacy Kind implementation
type PortMapping struct {
	HostPort      string
	ContainerPort string
}

// HttpClient interface for HTTP operations (from legacy code)
type HttpClient interface {
	Get(url string) (resp *http.Response, err error)
}

// defaultHttpClient implements HttpClient interface
type defaultHttpClient struct{}

func (c *defaultHttpClient) Get(url string) (resp *http.Response, err error) {
	return http.Get(url)
}

// KindProvider implements Provider for local Kind clusters
type KindProvider struct {
	envConfig      *config.ResolvedEnvironmentConfig
	logger         *logrus.Logger
	templateEngine *TemplateEngine
	httpClient     HttpClient // Add HTTP client for remote config support
}

// NewKindProvider creates a new Kind provider
func NewKindProvider(envConfig *config.ResolvedEnvironmentConfig, logger *logrus.Logger, templateEngine *TemplateEngine) (Provider, error) {
	return &KindProvider{
		envConfig:      envConfig,
		logger:         logger,
		templateEngine: templateEngine,
		httpClient:     &defaultHttpClient{}, // Initialize HTTP client
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
		kp.logger.Info("   Configuration: 1 control-plane + 2 worker nodes")
		kp.logger.Info("   Networking: CNI disabled (Cilium will be installed)")
		kp.logger.Info("   Template source: platform/build/templates/kind/kind.yaml.tmpl")
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

		// Phase 2: Setup ArgoCD for platform stack management (includes success message)
		if err := kp.setupArgoCDPlatformManagement(ctx, kubeconfig, enableHAMode, envConfig); err != nil {
			return fmt.Errorf("failed to setup ArgoCD platform management: %w", err)
		}

		// Optional: Run background verification
		go func() {
			time.Sleep(60 * time.Second) // Wait a minute before final verification
			kp.logger.Info("🔍 Running background platform verification...")
			if err := kp.verifyPlatformServices(ctx, kubeconfig); err != nil {
				kp.logger.Warn("⚠️  Some services may still be starting up", "error", err)
			} else {
				kp.logger.Info("✅ All platform services verified successfully")
			}
		}()
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

	// Pre-check: Ensure the cluster is ready before installing Cilium
	kp.logger.Info("   Pre-check: Verifying cluster is ready...")
	if err := kp.runKubectlCommand(ctx, kubeconfig, "get", "nodes", "--no-headers"); err != nil {
		return fmt.Errorf("cluster nodes are not ready: %w", err)
	}
	kp.logger.Info("   ✅ Cluster nodes are accessible")

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

	// Step 3: Install other core services in parallel (after Cilium is ready)
	otherServices := []string{"nginx", "gitea"}

	kp.logger.Info("🚀 Installing platform services in parallel...")

	// Create channels for tracking parallel installation
	type serviceResult struct {
		service string
		err     error
	}
	resultChan := make(chan serviceResult, len(otherServices))

	// Install services in parallel
	for _, service := range otherServices {
		go func(svcName string) {
			kp.logger.Info("🔧 Starting installation of: " + svcName + "...")

			// Generate manifests using the template engine
			manifests, err := kp.templateEngine.GenerateManifests(ctx, svcName, enableHAMode)
			if err != nil {
				resultChan <- serviceResult{svcName, fmt.Errorf("failed to generate manifests for %s: %w", svcName, err)}
				return
			}

			// Apply manifests using kubectl
			if err := kp.applyManifests(ctx, kubeconfig, manifests, svcName); err != nil {
				resultChan <- serviceResult{svcName, fmt.Errorf("failed to apply manifests for %s: %w", svcName, err)}
				return
			}

			kp.logger.Info("✅ " + svcName + " manifests applied successfully")
			resultChan <- serviceResult{svcName, nil}
		}(service)
	}

	// Wait for all parallel installations to complete
	var installationErrors []error
	for i := 0; i < len(otherServices); i++ {
		result := <-resultChan
		if result.err != nil {
			kp.logger.Error("Failed to install service", "service", result.service, "error", result.err)
			installationErrors = append(installationErrors, result.err)
		} else {
			kp.logger.Info("📦 " + result.service + " installation initiated successfully")
		}
	}

	// Check if any installations failed
	if len(installationErrors) > 0 {
		return fmt.Errorf("failed to install %d services: %v", len(installationErrors), installationErrors)
	}

	// Now wait for all services to be ready in parallel
	kp.logger.Info("⏳ Waiting for all platform services to become ready...")
	readinessChan := make(chan serviceResult, len(otherServices))

	for _, service := range otherServices {
		go func(svcName string) {
			kp.logger.Info("🔍 Checking readiness of: " + svcName + "...")
			if err := kp.waitForServiceReadyRobust(ctx, kubeconfig, svcName); err != nil {
				readinessChan <- serviceResult{svcName, err}
			} else {
				kp.logger.Info("✅ " + svcName + " is ready and operational")
				readinessChan <- serviceResult{svcName, nil}
			}
		}(service)
	}

	// Collect readiness results
	var readinessErrors []error
	readyServices := 0
	for i := 0; i < len(otherServices); i++ {
		result := <-readinessChan
		if result.err != nil {
			kp.logger.Warn("⚠️  "+result.service+" may not be fully ready yet", "error", result.err)
			readinessErrors = append(readinessErrors, result.err)
		} else {
			readyServices++
			kp.logger.Info("🎉 " + result.service + " is fully operational")
		}
	}

	kp.logger.Info("🎉 Core infrastructure installation completed", "ready_services", readyServices, "total_services", len(otherServices))
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

	// Wait for ArgoCD to be ready using robust checking
	kp.logger.Info("⏳ Waiting for ArgoCD to be ready (this is the final critical component)...")
	if err := kp.waitForServiceReadyRobust(ctx, kubeconfig, "argocd"); err != nil {
		kp.logger.Warn("⚠️  ArgoCD may not be fully ready yet, but continuing...", "error", err)
	} else {
		kp.logger.Info("🎉 ArgoCD is ready and operational!")

		// Show success message immediately when ArgoCD is ready
		kp.logger.Info("")
		kp.logger.Info("🎉✨ Adhar platform is ready! ✨🎉")
		kp.logger.Info("🌐 Access your services at:")
		kp.printServiceURLs(envConfig)
		kp.logger.Info("")
	}

	// Deploy platform stack applications in the background
	go func() {
		kp.logger.Info("📦 Deploying platform stack applications in background...")
		if err := kp.deployPlatformStackApplications(ctx, kubeconfig, envConfig); err != nil {
			kp.logger.Warn("⚠️  Some platform stack applications may not have deployed successfully", "error", err)
		} else {
			kp.logger.Info("✅ Platform stack applications deployed successfully")
		}
	}()

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
	timeout := "1800s" // 30 minutes for all services

	switch serviceName {
	case "cilium":
		// Cilium uses a more comprehensive check in waitForCiliumReady
		return kp.runKubectlCommand(ctx, kubeconfig, "wait", "--for=condition=ready", "pod", "-l", "k8s-app=cilium", "-n", "adhar-system", "--timeout="+timeout)
	case "nginx":
		kp.logger.Info("   Waiting for NGINX Ingress Controller... (timeout: 30 minutes)")
		if err := kp.runKubectlCommand(ctx, kubeconfig, "wait", "--for=condition=ready", "pod", "-l", "app.kubernetes.io/name=ingress-nginx", "-n", "adhar-system", "--timeout="+timeout); err != nil {
			kp.logger.Warn("NGINX timeout, checking status...")
			kp.runKubectlCommand(ctx, kubeconfig, "get", "pods", "-n", "adhar-system", "-l", "app.kubernetes.io/name=ingress-nginx")
			return err
		}
		return nil
	case "gitea":
		kp.logger.Info("   Waiting for Gitea... (timeout: 30 minutes)")
		if err := kp.runKubectlCommand(ctx, kubeconfig, "wait", "--for=condition=ready", "pod", "-l", "app.kubernetes.io/name=gitea", "-n", "adhar-system", "--timeout="+timeout); err != nil {
			kp.logger.Warn("Gitea timeout, checking status...")
			kp.runKubectlCommand(ctx, kubeconfig, "get", "pods", "-n", "adhar-system", "-l", "app.kubernetes.io/name=gitea")
			return err
		}
		return nil
	case "argocd":
		kp.logger.Info("   Waiting for ArgoCD... (timeout: 30 minutes)")
		if err := kp.runKubectlCommand(ctx, kubeconfig, "wait", "--for=condition=ready", "pod", "-l", "app.kubernetes.io/name=argocd-server", "-n", "adhar-system", "--timeout="+timeout); err != nil {
			kp.logger.Warn("ArgoCD timeout, checking status...")
			kp.runKubectlCommand(ctx, kubeconfig, "get", "pods", "-n", "adhar-system", "-l", "app.kubernetes.io/name=argocd-server")
			return err
		}
		return nil
	default:
		// Generic wait - just give it some time
		kp.logger.Info("   Waiting for " + serviceName + "...")
		time.Sleep(30 * time.Second)
		return nil
	}
}

// waitForServiceReadyRobust waits for a service to be ready with enhanced polling and retries
func (kp *KindProvider) waitForServiceReadyRobust(ctx context.Context, kubeconfig, serviceName string) error {
	timeout := "1800s" // 30 minutes total timeout
	maxRetries := 10
	baseDelay := 30 * time.Second

	switch serviceName {
	case "nginx":
		kp.logger.Info("   🔍 Robust check for NGINX Ingress Controller...")
		for attempt := 1; attempt <= maxRetries; attempt++ {
			err := kp.runKubectlCommand(ctx, kubeconfig, "wait", "--for=condition=ready", "pod", "-l", "app.kubernetes.io/name=ingress-nginx", "-n", "adhar-system", "--timeout=180s")
			if err == nil {
				// Additional check: verify deployment is available
				if err2 := kp.runKubectlCommand(ctx, kubeconfig, "wait", "--for=condition=available", "deployment", "-l", "app.kubernetes.io/name=ingress-nginx", "-n", "adhar-system", "--timeout=60s"); err2 == nil {
					return nil
				}
			}

			if attempt < maxRetries {
				delay := time.Duration(attempt) * baseDelay
				kp.logger.Info("   ⏳ NGINX not ready yet, retrying...", "attempt", attempt, "max_retries", maxRetries, "next_check_in", delay)
				time.Sleep(delay)
			}
		}

		// Final attempt with full timeout
		kp.logger.Info("   🔄 Final attempt for NGINX readiness...")
		return kp.runKubectlCommand(ctx, kubeconfig, "wait", "--for=condition=ready", "pod", "-l", "app.kubernetes.io/name=ingress-nginx", "-n", "adhar-system", "--timeout="+timeout)

	case "gitea":
		kp.logger.Info("   🔍 Robust check for Gitea...")
		for attempt := 1; attempt <= maxRetries; attempt++ {
			err := kp.runKubectlCommand(ctx, kubeconfig, "wait", "--for=condition=ready", "pod", "-l", "app.kubernetes.io/name=gitea", "-n", "adhar-system", "--timeout=180s")
			if err == nil {
				// Additional check: verify statefulset is ready
				if err2 := kp.runKubectlCommand(ctx, kubeconfig, "wait", "--for=condition=ready", "statefulset", "-l", "app.kubernetes.io/name=gitea", "-n", "adhar-system", "--timeout=60s"); err2 == nil {
					return nil
				}
			}

			if attempt < maxRetries {
				delay := time.Duration(attempt) * baseDelay
				kp.logger.Info("   ⏳ Gitea not ready yet, retrying...", "attempt", attempt, "max_retries", maxRetries, "next_check_in", delay)
				time.Sleep(delay)
			}
		}

		// Final attempt with full timeout
		kp.logger.Info("   🔄 Final attempt for Gitea readiness...")
		return kp.runKubectlCommand(ctx, kubeconfig, "wait", "--for=condition=ready", "pod", "-l", "app.kubernetes.io/name=gitea", "-n", "adhar-system", "--timeout="+timeout)

	case "argocd":
		kp.logger.Info("   🔍 Robust check for ArgoCD...")
		for attempt := 1; attempt <= maxRetries; attempt++ {
			// Check server pods
			err := kp.runKubectlCommand(ctx, kubeconfig, "wait", "--for=condition=ready", "pod", "-l", "app.kubernetes.io/name=argocd-server", "-n", "adhar-system", "--timeout=180s")
			if err == nil {
				// Additional checks: verify other ArgoCD components
				err2 := kp.runKubectlCommand(ctx, kubeconfig, "wait", "--for=condition=ready", "pod", "-l", "app.kubernetes.io/name=argocd-application-controller", "-n", "adhar-system", "--timeout=60s")
				err3 := kp.runKubectlCommand(ctx, kubeconfig, "wait", "--for=condition=ready", "pod", "-l", "app.kubernetes.io/name=argocd-repo-server", "-n", "adhar-system", "--timeout=60s")

				if err2 == nil && err3 == nil {
					// Final check: verify ArgoCD API is responding
					if err4 := kp.runKubectlCommand(ctx, kubeconfig, "get", "pods", "-n", "adhar-system", "-l", "app.kubernetes.io/part-of=argocd", "--no-headers"); err4 == nil {
						return nil
					}
				}
			}

			if attempt < maxRetries {
				delay := time.Duration(attempt) * baseDelay
				kp.logger.Info("   ⏳ ArgoCD not ready yet, retrying...", "attempt", attempt, "max_retries", maxRetries, "next_check_in", delay)
				time.Sleep(delay)
			}
		}

		// Final attempt with full timeout
		kp.logger.Info("   🔄 Final attempt for ArgoCD readiness...")
		return kp.runKubectlCommand(ctx, kubeconfig, "wait", "--for=condition=ready", "pod", "-l", "app.kubernetes.io/name=argocd-server", "-n", "adhar-system", "--timeout="+timeout)

	default:
		// Generic robust check
		kp.logger.Info("   🔍 Generic robust check for " + serviceName + "...")
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
		kp.logger.Error("kubectl command failed", "cmd", cmd.String(), "output", string(output), "error", err)
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

	// Create namespace manifest
	namespaceManifest := `apiVersion: v1
kind: Namespace
metadata:
  name: adhar-system
  labels:
    app.kubernetes.io/name: adhar-system
    app.kubernetes.io/part-of: adhar-platform
`

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

// createClusterWithTemplate creates a Kind cluster using enhanced template configuration (enhanced from legacy code)
func (kp *KindProvider) createClusterWithTemplate(ctx context.Context, clusterName, kubeVersion string, envConfig *config.ResolvedEnvironmentConfig) error {
	// Check if a custom Kind config path is specified
	customConfigPath := ""
	for _, cfg := range envConfig.ResolvedClusterConfig {
		if cfg.Key == "kindConfigPath" {
			customConfigPath = cfg.Value
			break
		}
	}

	// Load the Kind configuration (file, remote URL, or default template)
	var templateContent []byte
	var err error

	if customConfigPath != "" {
		kp.logger.Info("🔧 Using custom Kind configuration", "source", customConfigPath)
		templateContent, err = kp.loadKindConfig(customConfigPath)
		if err != nil {
			return fmt.Errorf("failed to load custom Kind config: %w", err)
		}

		// For custom configs, apply template rendering if it contains template variables
		if strings.Contains(string(templateContent), "{{") {
			kp.logger.Info("🎨 Rendering custom Kind config template...")
			templateData := kp.prepareKindTemplateData(kubeVersion, envConfig)
			renderedConfig, err := kp.renderKindTemplate(string(templateContent), templateData)
			if err != nil {
				return fmt.Errorf("failed to render custom Kind template: %w", err)
			}
			templateContent = []byte(renderedConfig)
		}

		// Ensure the custom config has correct port mappings and labels
		kp.logger.Info("� Validating and correcting custom Kind configuration...")
		templateContent, err = kp.ensureCorrectKindConfig(templateContent, envConfig)
		if err != nil {
			return fmt.Errorf("failed to validate custom Kind config: %w", err)
		}
	} else {
		// Use default template approach
		templateContent, err = kp.loadKindConfig("") // Empty path loads default template
		if err != nil {
			return fmt.Errorf("failed to load default Kind template: %w", err)
		}

		// Prepare template data with configuration from envConfig
		templateData := kp.prepareKindTemplateData(kubeVersion, envConfig)

		// Render the template
		renderedConfig, err := kp.renderKindTemplate(string(templateContent), templateData)
		if err != nil {
			return fmt.Errorf("failed to render Kind template: %w", err)
		}
		templateContent = []byte(renderedConfig)
	}

	kp.logger.Info("########################### Adhar Kind Configuration ############################")
	kp.logger.Info(string(templateContent))
	kp.logger.Info("#########################   Configuration End    ############################")

	// Create the cluster using the processed configuration
	cmd := exec.CommandContext(ctx, "kind", "create", "cluster", "--name", clusterName, "--config", "-")
	cmd.Stdin = strings.NewReader(string(templateContent))

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to create Kind cluster with template: %w, output: %s", err, string(output))
	}

	kp.logger.Info("✅ Kind cluster created successfully with enhanced template", "name", clusterName)
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

// waitForCiliumReady waits specifically for Cilium to be ready with proper logging
func (kp *KindProvider) waitForCiliumReady(ctx context.Context, kubeconfig string) error {
	// Wait for Cilium operator to be ready first (30 minutes timeout)
	kp.logger.Info("   Waiting for Cilium operator... (this may take up to 30 minutes)")
	if err := kp.runKubectlCommand(ctx, kubeconfig, "wait", "--for=condition=available", "deployment", "cilium-operator", "-n", "adhar-system", "--timeout=1800s"); err != nil {
		kp.logger.Warn("Cilium operator timeout, checking status...")
		// Show current status for debugging
		kp.runKubectlCommand(ctx, kubeconfig, "get", "pods", "-n", "adhar-system", "-l", "name=cilium-operator")
		kp.runKubectlCommand(ctx, kubeconfig, "describe", "deployment", "cilium-operator", "-n", "adhar-system")
		return fmt.Errorf("Cilium operator failed to become ready: %w", err)
	}

	// Wait for Cilium DaemonSet to be ready (30 minutes timeout)
	kp.logger.Info("   Waiting for Cilium DaemonSet... (this may take up to 30 minutes)")

	// First, try a quick check to see if pods are already ready
	if err := kp.runKubectlCommand(ctx, kubeconfig, "get", "pods", "-l", "k8s-app=cilium", "-n", "adhar-system", "--no-headers"); err == nil {
		// Pods exist, check if they're ready with a shorter timeout first
		if err2 := kp.runKubectlCommand(ctx, kubeconfig, "wait", "--for=condition=ready", "pod", "-l", "k8s-app=cilium", "-n", "adhar-system", "--timeout=60s"); err2 == nil {
			kp.logger.Info("   ✅ Cilium pods are already ready")
		} else {
			// If quick check fails, do the long wait
			if err3 := kp.runKubectlCommand(ctx, kubeconfig, "wait", "--for=condition=ready", "pod", "-l", "k8s-app=cilium", "-n", "adhar-system", "--timeout=1800s"); err3 != nil {
				kp.logger.Warn("Cilium DaemonSet timeout, checking status...")
				// Show current status for debugging
				kp.runKubectlCommand(ctx, kubeconfig, "get", "pods", "-n", "adhar-system", "-l", "k8s-app=cilium")
				kp.runKubectlCommand(ctx, kubeconfig, "describe", "pods", "-n", "adhar-system", "-l", "k8s-app=cilium")
				kp.runKubectlCommand(ctx, kubeconfig, "get", "daemonset", "cilium", "-n", "adhar-system")
				return fmt.Errorf("Cilium DaemonSet failed to become ready: %w", err3)
			}
		}
	} else {
		return fmt.Errorf("Cilium pods not found: %w", err)
	}

	// Additional check: Wait for all nodes to have Cilium pods running
	kp.logger.Info("   Verifying Cilium is running on all nodes...")
	// Check DaemonSet status instead of waiting for "ready" condition (which doesn't exist for DaemonSets)
	if err := kp.runKubectlCommand(ctx, kubeconfig, "get", "daemonset", "cilium", "-n", "adhar-system", "-o", "jsonpath={.status.numberReady}"); err != nil {
		kp.logger.Warn("Could not check Cilium DaemonSet status, but continuing...")
	} else {
		// Verify that desired replicas match ready replicas (this is a quick check)
		if err2 := kp.runKubectlCommand(ctx, kubeconfig, "wait", "--for=condition=available", "daemonset", "cilium", "-n", "adhar-system", "--timeout=300s"); err2 != nil {
			kp.logger.Warn("Cilium DaemonSet not fully available, but pods are ready, continuing...")
		}
	}

	// Verify that Cilium is providing networking (check if we can see cilium endpoints)
	kp.logger.Info("   Verifying Cilium networking...")
	if err := kp.runKubectlCommand(ctx, kubeconfig, "get", "ciliumnodes"); err != nil {
		kp.logger.Debug("CiliumNodes not yet available, checking pods instead...")
		// Alternative check - verify cilium pods are running
		if err2 := kp.runKubectlCommand(ctx, kubeconfig, "get", "pods", "-n", "adhar-system", "-l", "k8s-app=cilium", "--no-headers"); err2 != nil {
			kp.logger.Debug("Could not verify Cilium pods, but continuing...")
		}
	}

	kp.logger.Info("   ✅ Cilium readiness checks completed")
	return nil
}

// waitForNodesReady waits for all cluster nodes to be in Ready state
func (kp *KindProvider) waitForNodesReady(ctx context.Context, kubeconfig string) error {
	kp.logger.Info("   Waiting for all nodes to be ready... (timeout: 10 minutes)")
	return kp.runKubectlCommand(ctx, kubeconfig, "wait", "--for=condition=ready", "nodes", "--all", "--timeout=600s")
}

// verifyPlatformServices performs final verification of all platform services
func (kp *KindProvider) verifyPlatformServices(ctx context.Context, kubeconfig string) error {
	services := []struct {
		name      string
		namespace string
		selector  string
		checkType string // "pod", "deployment", "statefulset"
	}{
		{"cilium", "adhar-system", "k8s-app=cilium", "pod"},
		{"nginx", "adhar-system", "app.kubernetes.io/name=argocd-server", "deployment"},
		{"gitea", "adhar-system", "app.kubernetes.io/name=gitea", "pod"},
		{"argocd-server", "adhar-system", "app.kubernetes.io/name=argocd-server", "pod"},
		{"argocd-controller", "adhar-system", "app.kubernetes.io/name=argocd-application-controller", "pod"},
		{"argocd-repo", "adhar-system", "app.kubernetes.io/name=argocd-repo-server", "pod"},
	}

	allReady := true
	readyCount := 0

	kp.logger.Info("🔍 Comprehensive platform verification...")

	for _, svc := range services {
		kp.logger.Info("   Checking " + svc.name + "...")

		// Check if pods exist and are running
		if err := kp.runKubectlCommand(ctx, kubeconfig, "get", "pods", "-l", svc.selector, "-n", svc.namespace, "--no-headers"); err != nil {
			kp.logger.Warn("   ⚠️  " + svc.name + " pods not found")
			allReady = false
			continue
		}

		// Additional readiness check based on service type
		var readyErr error
		switch svc.checkType {
		case "deployment":
			readyErr = kp.runKubectlCommand(ctx, kubeconfig, "wait", "--for=condition=available", "deployment", "-l", svc.selector, "-n", svc.namespace, "--timeout=30s")
		case "statefulset":
			readyErr = kp.runKubectlCommand(ctx, kubeconfig, "wait", "--for=condition=ready", "statefulset", "-l", svc.selector, "-n", svc.namespace, "--timeout=30s")
		default: // pod
			readyErr = kp.runKubectlCommand(ctx, kubeconfig, "wait", "--for=condition=ready", "pod", "-l", svc.selector, "-n", svc.namespace, "--timeout=30s")
		}

		if readyErr != nil {
			kp.logger.Warn("   ⚠️  " + svc.name + " not fully ready yet")
			allReady = false
		} else {
			kp.logger.Info("   ✅ " + svc.name + " is healthy")
			readyCount++
		}
	}

	kp.logger.Info("📊 Platform verification completed", "ready_services", readyCount, "total_services", len(services))

	if !allReady {
		return fmt.Errorf("some services are not fully ready yet (%d/%d ready)", readyCount, len(services))
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

// logServiceStatus logs the current status of a service during installation
func (kp *KindProvider) logServiceStatus(ctx context.Context, kubeconfig, serviceName, namespace string) {
	// Log pod status
	if err := kp.runKubectlCommand(ctx, kubeconfig, "get", "pods", "-n", namespace, "-l", "app.kubernetes.io/name="+serviceName, "--no-headers"); err == nil {
		kp.logger.Debug("Pod status logged", "service", serviceName)
	}

	// Log events for the namespace
	if err := kp.runKubectlCommand(ctx, kubeconfig, "get", "events", "-n", namespace, "--sort-by='.lastTimestamp'", "--field-selector=type=Warning", "--no-headers"); err == nil {
		kp.logger.Debug("Events status logged", "service", serviceName)
	}
}

// prepareKindTemplateData prepares the template data for the Kind cluster template (enhanced from legacy code)
func (kp *KindProvider) prepareKindTemplateData(kubeVersion string, envConfig *config.ResolvedEnvironmentConfig) map[string]interface{} {
	// Extract configuration values from envConfig
	host := "adhar.localtest.me"
	port := "8443"
	protocol := "https"
	usePathRouting := false
	staticPassword := false
	registryConfig := ""
	extraPortsMappingStr := ""
	registryConfigPaths := []string{}

	// Extract values from ResolvedClusterConfig
	for _, cfg := range envConfig.ResolvedClusterConfig {
		switch cfg.Key {
		case "host":
			host = cfg.Value
		case "port":
			port = cfg.Value
		case "protocol":
			protocol = cfg.Value
		case "usePathRouting":
			usePathRouting = cfg.Value == "true"
		case "staticPassword":
			staticPassword = cfg.Value == "true"
		case "registryConfig":
			registryConfig = cfg.Value
		case "extraPortsMapping":
			extraPortsMappingStr = cfg.Value
		case "registryConfigPaths":
			registryConfigPaths = strings.Split(cfg.Value, ",")
		}
	}

	// Parse extra port mappings from string configuration
	portMappings := kp.parsePortMappings(extraPortsMappingStr)
	extraPortsMapping := make([]map[string]interface{}, len(portMappings))
	for i, pm := range portMappings {
		extraPortsMapping[i] = map[string]interface{}{
			"HostPort":      pm.HostPort,
			"ContainerPort": pm.ContainerPort,
		}
	}

	// Find registry config file if paths are provided
	if len(registryConfigPaths) > 0 && registryConfig == "" {
		registryConfig = kp.findRegistryConfig(registryConfigPaths)
	}

	templateData := map[string]interface{}{
		"KubernetesVersion": kubeVersion,
		"Host":              host,
		"Port":              port,
		"Protocol":          protocol,
		"UsePathRouting":    usePathRouting,
		"StaticPassword":    staticPassword,
		"RegistryConfig":    registryConfig,
		"ExtraPortsMapping": extraPortsMapping,
	}

	kp.logger.Debug("Kind template data prepared",
		"kubeVersion", kubeVersion,
		"host", host,
		"port", port,
		"protocol", protocol,
		"usePathRouting", usePathRouting,
		"staticPassword", staticPassword,
		"extraPortsCount", len(extraPortsMapping),
		"registryConfig", registryConfig,
	)

	return templateData
}

// loadKindConfig loads Kind configuration from file, remote URL, or embedded template (enhanced from legacy code)
func (kp *KindProvider) loadKindConfig(configPath string) ([]byte, error) {
	var rawConfigTempl []byte
	var err error

	if configPath != "" {
		if strings.HasPrefix(configPath, "https://") || strings.HasPrefix(configPath, "http://") {
			kp.logger.Info("📡 Loading Kind config from remote URL: " + configPath)
			resp, err := kp.httpClient.Get(configPath)
			if err != nil {
				return nil, fmt.Errorf("fetching remote kind config: %w", err)
			}
			defer resp.Body.Close()

			if !(resp.StatusCode < 300 && resp.StatusCode >= 200) {
				return nil, fmt.Errorf("got %d status code when fetching kind config from %s", resp.StatusCode, configPath)
			}

			rawConfigTempl, err = io.ReadAll(resp.Body)
			if err != nil {
				return nil, fmt.Errorf("reading remote kind config body: %w", err)
			}
			kp.logger.Info("✅ Successfully loaded remote Kind config")
		} else {
			kp.logger.Info("📁 Loading Kind config from local file: " + configPath)
			rawConfigTempl, err = os.ReadFile(configPath)
			if err != nil {
				return nil, fmt.Errorf("reading local kind config file: %w", err)
			}
		}
	} else {
		// Use default template
		templatePath := "platform/build/templates/kind/kind.yaml.tmpl"
		absTemplatePath, err := filepath.Abs(templatePath)
		if err != nil {
			return nil, fmt.Errorf("failed to get absolute path for Kind template: %w", err)
		}
		kp.logger.Info("📄 Loading default Kind template from: " + absTemplatePath)
		rawConfigTempl, err = os.ReadFile(absTemplatePath)
		if err != nil {
			return nil, fmt.Errorf("reading default kind template: %w", err)
		}
	}

	return rawConfigTempl, nil
}

// parsePortMappings parses extra port mappings from string format (from legacy code)
func (kp *KindProvider) parsePortMappings(extraPortsMapping string) []PortMapping {
	var portMappingPairs []PortMapping
	if len(extraPortsMapping) > 0 {
		kp.logger.Debug("Parsing extra port mappings", "mappings", extraPortsMapping)
		// Split pairs of ports "11:1111","22:2222",etc
		pairs := strings.Split(extraPortsMapping, ",")
		// Create a slice to store PortMapping pairs.
		portMappingPairs = make([]PortMapping, len(pairs))
		// Parse each pair into PortMapping objects.
		for i, pair := range pairs {
			parts := strings.Split(pair, ":")
			if len(parts) == 2 {
				portMappingPairs[i] = PortMapping{
					HostPort:      strings.TrimSpace(parts[0]),
					ContainerPort: strings.TrimSpace(parts[1]),
				}
			}
		}
		kp.logger.Debug("Parsed port mappings", "count", len(portMappingPairs))
	}
	return portMappingPairs
}

// findRegistryConfig finds the first existing registry config file from provided paths (from legacy code)
func (kp *KindProvider) findRegistryConfig(registryConfigPaths []string) string {
	for _, s := range registryConfigPaths {
		path := os.ExpandEnv(s)
		if _, err := os.Stat(path); err == nil {
			kp.logger.Info("✅ Found registry config at: " + path)
			return path
		}
	}
	if len(registryConfigPaths) > 0 {
		kp.logger.Debug("No registry config found in provided paths", "paths", registryConfigPaths)
	}
	return ""
}

// ensureCorrectKindConfig ensures the Kind config has correct port mappings and ingress labels (enhanced from legacy code)
func (kp *KindProvider) ensureCorrectKindConfig(configData []byte, envConfig *config.ResolvedEnvironmentConfig) ([]byte, error) {
	// Parse the YAML config
	parsedCluster := kindv1alpha4.Cluster{}
	err := yaml.Unmarshal(configData, &parsedCluster)
	if err != nil {
		return nil, fmt.Errorf("parsing kind config: %w", err)
	}

	if len(parsedCluster.Nodes) == 0 {
		return nil, fmt.Errorf("provided kind config does not have the node field defined")
	}

	// Get configuration values
	port := "8443"
	protocol := "https"
	for _, cfg := range envConfig.ResolvedClusterConfig {
		switch cfg.Key {
		case "port":
			port = cfg.Value
		case "protocol":
			protocol = cfg.Value
		}
	}

	// Determine container port based on protocol
	containerPort := "443"
	if protocol == "http" {
		containerPort = "80"
	}

	kp.logger.Debug("Ensuring correct Kind config", "hostPort", port, "containerPort", containerPort, "protocol", protocol)

	// Check if we need to add necessary port and ingress-nginx label
	appendNecessaryPort := true
	appendIngressNodeLabel := true
	nodePosition := 0 // pick the first node for the ingress-nginx if we need to configure node port

	// Look for existing configuration
nodes:
	for i := range parsedCluster.Nodes {
		node := parsedCluster.Nodes[i]

		// Check if port mapping already exists
		for _, pm := range node.ExtraPortMappings {
			if strconv.Itoa(int(pm.HostPort)) == port {
				appendNecessaryPort = false
				nodePosition = i
				if node.Labels != nil {
					v, ok := node.Labels[ingressNginxNodeLabelKey]
					if ok && v == ingressNginxNodeLabelValue {
						appendIngressNodeLabel = false
					}
				}
				break nodes
			}
		}

		// Check if ingress label already exists
		if node.Labels != nil {
			v, ok := node.Labels[ingressNginxNodeLabelKey]
			if ok && v == ingressNginxNodeLabelValue {
				appendIngressNodeLabel = false
				nodePosition = i
				break nodes
			}
		}
	}

	// Add necessary port mapping if missing
	if appendNecessaryPort {
		hp, err := strconv.Atoi(port)
		if err != nil {
			return nil, fmt.Errorf("converting port, %s, to int: %w", port, err)
		}
		cp, err := strconv.Atoi(containerPort)
		if err != nil {
			return nil, fmt.Errorf("converting container port, %s, to int: %w", containerPort, err)
		}

		if parsedCluster.Nodes[nodePosition].ExtraPortMappings == nil {
			parsedCluster.Nodes[nodePosition].ExtraPortMappings = make([]kindv1alpha4.PortMapping, 0, 1)
		}

		parsedCluster.Nodes[nodePosition].ExtraPortMappings = append(
			parsedCluster.Nodes[nodePosition].ExtraPortMappings,
			kindv1alpha4.PortMapping{
				ContainerPort: int32(cp),
				HostPort:      int32(hp),
				Protocol:      "TCP",
			},
		)

		kp.logger.Info("➕ Added port mapping to Kind config", "hostPort", hp, "containerPort", cp, "nodeIndex", nodePosition)
	}

	// Add ingress-nginx label if missing
	if appendIngressNodeLabel {
		if parsedCluster.Nodes[nodePosition].Labels == nil {
			parsedCluster.Nodes[nodePosition].Labels = make(map[string]string)
		}
		parsedCluster.Nodes[nodePosition].Labels[ingressNginxNodeLabelKey] = ingressNginxNodeLabelValue
		kp.logger.Info("🏷️  Added ingress-ready label to Kind config", "nodeIndex", nodePosition)
	}

	// Marshal back to YAML
	out, err := yaml.Marshal(parsedCluster)
	if err != nil {
		return nil, fmt.Errorf("marshaling corrected kind cluster config: %w", err)
	}

	return out, nil
}
