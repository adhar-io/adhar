package domain

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"adhar-io/adhar/platform/types"
)

// Manager handles domain setup and management for clusters
type Manager struct {
	config     *types.DomainConfig
	kubeconfig string
}

// NewManager creates a new domain manager
func NewManager(domainConfig *types.DomainConfig, kubeconfig string) *Manager {
	return &Manager{
		config:     domainConfig,
		kubeconfig: kubeconfig,
	}
}

// SetupDomain sets up domain management for a cluster
func (m *Manager) SetupDomain(ctx context.Context, cluster *types.Cluster) error {
	// Check if we should suppress output (called from platform setup)
	suppressOutput := os.Getenv("ADHAR_PLATFORM_SETUP") == "true"

	if !suppressOutput {
		fmt.Printf("Setting up domain management for cluster '%s'...\n", cluster.Name)
	}

	// Determine domain based on provider type
	domain := m.getDomainForCluster(cluster)
	if !suppressOutput {
		fmt.Printf("Using domain: %s\n", domain)
	}

	// Install cert-manager if TLS is enabled
	if m.config.TLS.Enabled {
		if !suppressOutput {
			fmt.Printf("Installing cert-manager...\n")
		}
		if err := m.installCertManager(ctx); err != nil {
			return fmt.Errorf("failed to install cert-manager: %w", err)
		}

		// Create Let's Encrypt issuer
		if err := m.createLetsEncryptIssuer(ctx, domain); err != nil {
			return fmt.Errorf("failed to create Let's Encrypt issuer: %w", err)
		}
	}

	// Install external-dns for production clusters
	if cluster.Provider != "kind" && m.config.DNS.Provider != "" {
		fmt.Printf("Installing external-dns...\n")
		if err := m.installExternalDNS(ctx); err != nil {
			return fmt.Errorf("failed to install external-dns: %w", err)
		}
	}

	// Install and configure ingress controller
	if !suppressOutput {
		fmt.Printf("Installing %s ingress controller...\n", m.config.Ingress.Provider)
	}
	if err := m.installIngressController(ctx, cluster); err != nil {
		return fmt.Errorf("failed to install ingress controller: %w", err)
	}

	// Configure CoreDNS for local resolution (Kind clusters)
	if cluster.Provider == "kind" {
		if !suppressOutput {
			fmt.Printf("Configuring CoreDNS for local domain resolution...\n")
		}
		if err := m.setupCoreDNSForKind(ctx, domain); err != nil {
			if !suppressOutput {
				fmt.Printf("⚠️  Warning: Failed to configure CoreDNS: %v\n", err)
			}
		} else {
			if !suppressOutput {
				fmt.Printf("✓ CoreDNS configured for %s\n", domain)
			}
		}
	}

	// Store domain configuration in cluster
	if err := m.storeClusterConfig(ctx, cluster, domain); err != nil {
		return fmt.Errorf("failed to store cluster configuration: %w", err)
	}

	if !suppressOutput {
		fmt.Printf("✓ Domain management setup completed for %s\n", domain)
	}
	return nil
}

// getDomainForCluster returns the appropriate domain for the cluster
func (m *Manager) getDomainForCluster(cluster *types.Cluster) string {
	if cluster.Provider == "kind" {
		// Use base domain or fallback for local development
		if m.config.BaseDomain != "" && m.config.BaseDomain != "cloud.adhar.io" {
			return m.config.BaseDomain
		}
		return "adhar.localtest.me"
	}

	// Use production domain
	if m.config.BaseDomain != "" {
		return m.config.BaseDomain
	}

	// Fallback to cluster-specific domain
	return fmt.Sprintf("%s.%s.adhar.dev", cluster.Name, cluster.Provider)
}

// installCertManager installs cert-manager using Helm or kubectl
func (m *Manager) installCertManager(ctx context.Context) error {
	// Check if cert-manager is already installed
	cmd := exec.CommandContext(ctx, "kubectl", "get", "namespace", "cert-manager")
	if err := cmd.Run(); err == nil {
		fmt.Printf("cert-manager already installed, skipping...\n")
		return nil
	}

	// Install cert-manager using kubectl
	certManagerURL := "https://github.com/cert-manager/cert-manager/releases/download/v1.13.1/cert-manager.yaml"

	cmd = exec.CommandContext(ctx, "kubectl", "apply", "-f", certManagerURL)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to install cert-manager: %w\nOutput: %s", err, string(output))
	}

	// Wait for cert-manager to be ready
	fmt.Printf("Waiting for cert-manager to be ready...\n")
	cmd = exec.CommandContext(ctx, "kubectl", "wait",
		"--namespace", "cert-manager",
		"--for=condition=ready", "pod",
		"--selector=app.kubernetes.io/instance=cert-manager",
		"--timeout=300s")

	output, err = cmd.CombinedOutput()
	if err != nil {
		fmt.Printf("⚠️  Warning: Timeout waiting for cert-manager: %v\n", err)
	}

	return nil
}

// createLetsEncryptIssuer creates a Let's Encrypt ClusterIssuer
func (m *Manager) createLetsEncryptIssuer(ctx context.Context, domain string) error {
	email := m.config.TLS.Email
	if email == "" {
		email = fmt.Sprintf("admin@%s", domain)
	}

	environment := m.config.TLS.Environment
	if environment == "" {
		environment = "staging"
	}

	issuerName := "letsencrypt" // Default issuer name since we removed IssuerName field
	if issuerName == "" {
		issuerName = "letsencrypt"
	}

	// Determine Let's Encrypt server URL
	serverURL := "https://acme-staging-v02.api.letsencrypt.org/directory"
	if environment == "production" {
		serverURL = "https://acme-v02.api.letsencrypt.org/directory"
		issuerName = issuerName + "-prod"
	}

	issuerYAML := fmt.Sprintf(`apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: %s
spec:
  acme:
    server: %s
    email: %s
    privateKeySecretRef:
      name: %s-private-key
    solvers:
    - http01:
        ingress:
          class: %s
`, issuerName, serverURL, email, issuerName, m.config.Ingress.Provider)

	// Apply the issuer
	cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(issuerYAML)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to create Let's Encrypt issuer: %w\nOutput: %s", err, string(output))
	}

	fmt.Printf("✓ Created Let's Encrypt issuer: %s\n", issuerName)
	return nil
}

// installExternalDNS installs external-dns for automatic DNS management
func (m *Manager) installExternalDNS(ctx context.Context) error {
	// Check if external-dns is already installed
	cmd := exec.CommandContext(ctx, "kubectl", "get", "deployment", "external-dns", "-n", "kube-system")
	if err := cmd.Run(); err == nil {
		fmt.Printf("external-dns already installed, skipping...\n")
		return nil
	}

	// Create external-dns configuration based on provider
	externalDNSYAML := m.generateExternalDNSConfig()

	// Apply external-dns configuration
	cmd = exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(externalDNSYAML)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to install external-dns: %w\nOutput: %s", err, string(output))
	}

	fmt.Printf("✓ Installed external-dns for provider: %s\n", m.config.DNS.Provider)
	return nil
}

// generateExternalDNSConfig generates external-dns configuration based on DNS provider
func (m *Manager) generateExternalDNSConfig() string {
	provider := m.config.DNS.Provider
	var domainFilters []string
	if m.config.BaseDomain != "" {
		domainFilters = []string{m.config.BaseDomain}
	}

	// Base external-dns configuration
	config := fmt.Sprintf(`apiVersion: v1
kind: ServiceAccount
metadata:
  name: external-dns
  namespace: kube-system
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: external-dns
rules:
- apiGroups: [""]
  resources: ["services","endpoints","pods"]
  verbs: ["get","watch","list"]
- apiGroups: ["extensions","networking.k8s.io"]
  resources: ["ingresses"]
  verbs: ["get","watch","list"]
- apiGroups: [""]
  resources: ["nodes"]
  verbs: ["list","watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: external-dns-viewer
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: external-dns
subjects:
- kind: ServiceAccount
  name: external-dns
  namespace: kube-system
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: external-dns
  namespace: kube-system
spec:
  strategy:
    type: Recreate
  selector:
    matchLabels:
      app: external-dns
  template:
    metadata:
      labels:
        app: external-dns
    spec:
      serviceAccountName: external-dns
      containers:
      - name: external-dns
        image: k8s.gcr.io/external-dns/external-dns:v0.13.6
        args:
        - --source=service
        - --source=ingress
        - --provider=%s`, provider)

	// Add domain filters if specified
	for _, domain := range domainFilters {
		config += fmt.Sprintf("\n        - --domain-filter=%s", domain)
	}

	// Add provider-specific configuration
	switch provider {
	case "cloudflare":
		config += `
        - --cloudflare-proxied
        env:
        - name: CF_API_TOKEN
          valueFrom:
            secretKeyRef:
              name: cloudflare-credentials
              key: api-token`
	case "route53":
		config += `
        env:
        - name: AWS_ACCESS_KEY_ID
          valueFrom:
            secretKeyRef:
              name: aws-credentials
              key: access-key-id
        - name: AWS_SECRET_ACCESS_KEY
          valueFrom:
            secretKeyRef:
              name: aws-credentials
              key: secret-access-key`
	}

	return config
}

// installIngressController installs the specified ingress controller
func (m *Manager) installIngressController(ctx context.Context, cluster *types.Cluster) error {
	controller := m.config.Ingress.Provider
	if controller == "" {
		controller = "nginx"
	}

	switch controller {
	case "nginx":
		return m.installNginxIngress(ctx, cluster)
	case "traefik":
		return m.installTraefikIngress(ctx, cluster)
	default:
		return fmt.Errorf("unsupported ingress controller: %s", controller)
	}
}

// installNginxIngress installs NGINX ingress controller
func (m *Manager) installNginxIngress(ctx context.Context, cluster *types.Cluster) error {
	// Check if nginx-ingress is already installed
	cmd := exec.CommandContext(ctx, "kubectl", "get", "namespace", "ingress-nginx")
	if err := cmd.Run(); err == nil {
		fmt.Printf("nginx-ingress already installed, skipping...\n")
		return nil
	}

	var ingressURL string
	if cluster.Provider == "kind" {
		// Use Kind-specific NGINX ingress
		ingressURL = "https://raw.githubusercontent.com/kubernetes/ingress-nginx/main/deploy/static/provider/kind/deploy.yaml"
	} else {
		// Use cloud provider NGINX ingress
		ingressURL = "https://raw.githubusercontent.com/kubernetes/ingress-nginx/main/deploy/static/provider/cloud/deploy.yaml"
	}

	cmd = exec.CommandContext(ctx, "kubectl", "apply", "-f", ingressURL)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to install nginx-ingress: %w\nOutput: %s", err, string(output))
	}

	// Wait for ingress controller to be ready
	fmt.Printf("Waiting for NGINX ingress controller to be ready...\n")
	cmd = exec.CommandContext(ctx, "kubectl", "wait",
		"--namespace", "ingress-nginx",
		"--for=condition=ready", "pod",
		"--selector=app.kubernetes.io/component=controller",
		"--timeout=300s")

	output, err = cmd.CombinedOutput()
	if err != nil {
		fmt.Printf("⚠️  Warning: Timeout waiting for NGINX ingress: %v\n", err)
	}

	fmt.Printf("✓ NGINX ingress controller installed\n")
	return nil
}

// installTraefikIngress installs Traefik ingress controller
func (m *Manager) installTraefikIngress(ctx context.Context, cluster *types.Cluster) error {
	// This would implement Traefik installation
	// For now, return an error as it's not implemented
	return fmt.Errorf("Traefik ingress controller installation not yet implemented")
}

// storeClusterConfig stores domain configuration in the cluster as ConfigMaps and Secrets
func (m *Manager) storeClusterConfig(ctx context.Context, cluster *types.Cluster, domain string) error {
	// Create adhar-system namespace
	namespaceYAML := `apiVersion: v1
kind: Namespace
metadata:
  name: adhar-system
  labels:
    name: adhar-system
    adhar.io/managed-by: adhar`

	cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(namespaceYAML)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to create adhar-system namespace: %w", err)
	}

	// Create domain configuration ConfigMap
	configMapYAML := fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: adhar-domain-config
  namespace: adhar-system
  labels:
    adhar.io/managed-by: adhar
data:
  domain: "%s"
  provider: "%s"
  tls-enabled: "%t"
  tls-environment: "%s"
  ingress-controller: "%s"
  dns-provider: "%s"`, domain, cluster.Provider, m.config.TLS.Enabled, m.config.TLS.Environment, m.config.Ingress.Provider, m.config.DNS.Provider)

	cmd = exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(configMapYAML)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to create domain configuration ConfigMap: %w", err)
	}

	fmt.Printf("✓ Stored domain configuration in cluster\n")
	return nil
}

// setupCoreDNSForKind configures CoreDNS for local domain resolution in Kind clusters
func (m *Manager) setupCoreDNSForKind(ctx context.Context, domain string) error {
	// Get current CoreDNS ConfigMap
	cmd := exec.CommandContext(ctx, "kubectl", "get", "configmap", "coredns", "-n", "kube-system", "-o", "yaml")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to get CoreDNS ConfigMap: %w", err)
	}

	// Check if our custom configuration already exists
	if strings.Contains(string(output), domain) {
		fmt.Printf("CoreDNS already configured for %s\n", domain)
		return nil
	}

	// Create custom CoreDNS configuration for local domain
	coreDNSConfig := fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: coredns-custom
  namespace: kube-system
data:
  %s.server: |
    %s:53 {
        errors
        cache 30
        reload
        loadbalance
        rewrite name exact %s ingress-nginx-controller.ingress-nginx.svc.cluster.local
        rewrite stop {
            name regex (.*).%s ingress-nginx-controller.ingress-nginx.svc.cluster.local answer auto
        }
        forward . /etc/resolv.conf
    }`, domain, domain, domain, domain)

	// Apply the custom CoreDNS configuration
	cmd = exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(coreDNSConfig)
	output, err = cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to apply CoreDNS configuration: %w\nOutput: %s", err, string(output))
	}

	// Restart CoreDNS to pick up the new configuration
	cmd = exec.CommandContext(ctx, "kubectl", "rollout", "restart", "deployment/coredns", "-n", "kube-system")
	output, err = cmd.CombinedOutput()
	if err != nil {
		// Don't fail if restart fails, just warn
		fmt.Printf("⚠️  Warning: Failed to restart CoreDNS: %v\n", err)
	}

	return nil
}

// CleanupDomain removes domain management components from a cluster
func (m *Manager) CleanupDomain(ctx context.Context, cluster *types.Cluster) error {
	fmt.Printf("Cleaning up domain management for cluster '%s'...\n", cluster.Name)

	// Remove adhar-system namespace (this will clean up ConfigMaps and Secrets)
	cmd := exec.CommandContext(ctx, "kubectl", "delete", "namespace", "adhar-system", "--ignore-not-found=true")
	if err := cmd.Run(); err != nil {
		fmt.Printf("⚠️  Warning: Failed to clean up adhar-system namespace: %v\n", err)
	}

	fmt.Printf("✓ Domain management cleanup completed\n")
	return nil
}
