package build

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

// TemplateEngine handles KCL-based template processing
type TemplateEngine struct {
	templatesDir string
	logger       *logrus.Logger
}

// NewTemplateEngine creates a new template engine
func NewTemplateEngine(logger *logrus.Logger) *TemplateEngine {
	return &TemplateEngine{
		templatesDir: "platform/build/templates/platform-apps",
		logger:       logger,
	}
}

// PlatformAppConfig represents configuration for a platform application
type PlatformAppConfig struct {
	Name                string                 `yaml:"name"`
	Replicas            int                    `yaml:"replicas"`
	Resources           ResourceConfig         `yaml:"resources"`
	AdditionalConfig    map[string]interface{} `yaml:"additional_config,omitempty"`
	PostgreSQLReplicas  int                    `yaml:"postgresql_replicas,omitempty"`
	RedisReplicas       int                    `yaml:"redis_replicas,omitempty"`
	ServerReplicas      int                    `yaml:"server_replicas,omitempty"`
	ControllerReplicas  int                    `yaml:"controller_replicas,omitempty"`
	OperatorReplicas    int                    `yaml:"operator_replicas,omitempty"`
	HubbleRelayReplicas int                    `yaml:"hubble_relay_replicas,omitempty"`
	HubbleUIReplicas    int                    `yaml:"hubble_ui_replicas,omitempty"`
	EnableEncryption    bool                   `yaml:"enable_encryption,omitempty"`
	EnableL7Proxy       bool                   `yaml:"enable_l7_proxy,omitempty"`
}

// ResourceConfig represents resource constraints
type ResourceConfig struct {
	CPURequest    string `yaml:"cpu_request"`
	MemoryRequest string `yaml:"memory_request"`
	CPULimit      string `yaml:"cpu_limit"`
	MemoryLimit   string `yaml:"memory_limit"`
}

// LoadKCLConfig loads KCL configuration for a specific platform app and mode
func (te *TemplateEngine) LoadKCLConfig(ctx context.Context, appName string, enableHAMode bool) (*PlatformAppConfig, error) {
	mode := "local"
	if enableHAMode {
		mode = "production"
	}

	// Use KCL to extract configuration
	kclConfigPath := filepath.Join(te.templatesDir, "config.k")

	// Build KCL query to extract specific app configuration
	query := fmt.Sprintf("%s_config.%s", appName, mode)

	cmd := exec.CommandContext(ctx, "kcl", "run", kclConfigPath, "-d", query)
	output, err := cmd.Output()
	if err != nil {
		// Fallback to hardcoded configuration if KCL is not available
		te.logger.Info(fmt.Sprintf("KCL not available for %s (%s mode), using fallback configuration", appName, mode))
		return te.getFallbackConfig(appName, enableHAMode), nil
	}

	// Parse KCL output (which should be YAML/JSON)
	var config PlatformAppConfig
	if err := yaml.Unmarshal(output, &config); err != nil {
		return nil, fmt.Errorf("failed to parse KCL output: %w", err)
	}

	config.Name = appName
	return &config, nil
}

// getFallbackConfig provides hardcoded fallback configuration when KCL is not available
func (te *TemplateEngine) getFallbackConfig(appName string, enableHAMode bool) *PlatformAppConfig {
	localResources := ResourceConfig{
		CPURequest:    "100m",
		MemoryRequest: "256Mi",
		CPULimit:      "500m",
		MemoryLimit:   "512Mi",
	}

	productionResources := ResourceConfig{
		CPURequest:    "500m",
		MemoryRequest: "1Gi",
		CPULimit:      "2",
		MemoryLimit:   "4Gi",
	}

	config := &PlatformAppConfig{
		Name: appName,
	}

	if enableHAMode {
		config.Resources = productionResources
		switch appName {
		case "gitea":
			config.Replicas = 2
			config.PostgreSQLReplicas = 2
			config.RedisReplicas = 3
		case "argocd":
			config.ServerReplicas = 2
			config.ControllerReplicas = 2
		case "nginx":
			config.Replicas = 2
		case "cilium":
			config.OperatorReplicas = 2
			config.HubbleRelayReplicas = 2
			config.HubbleUIReplicas = 2
			config.EnableEncryption = true
			config.EnableL7Proxy = true
		case "crossplane":
			config.Replicas = 2
		}
	} else {
		config.Resources = localResources
		config.Replicas = 1
		config.PostgreSQLReplicas = 1
		config.RedisReplicas = 1
		config.ServerReplicas = 1
		config.ControllerReplicas = 1
		config.OperatorReplicas = 1
		config.HubbleRelayReplicas = 1
		config.HubbleUIReplicas = 1
		config.EnableEncryption = false
		config.EnableL7Proxy = false
	}

	return config
}

// GenerateManifests generates Kubernetes manifests for a platform app using KCL config and YAML templates
func (te *TemplateEngine) GenerateManifests(ctx context.Context, appName string, enableHAMode bool) (string, error) {
	// Force local mode for Kind clusters (non-HA) - always use minimal replica configuration

	// Load KCL configuration in local mode
	config, err := te.LoadKCLConfig(ctx, appName, false) // Always use local mode for Kind
	if err != nil {
		return "", fmt.Errorf("failed to load KCL config: %w", err)
	}

	// Load base YAML template from controllers directory (the actual manifest files)
	baseYAMLPath := filepath.Join("platform/controllers/adharplatform/resources", appName, "install.yaml")
	baseYAML, err := os.ReadFile(baseYAMLPath)
	if err != nil {
		return "", fmt.Errorf("failed to read base YAML template: %w", err)
	}

	// Apply configuration patches using Kustomize overlays for local development
	return te.applyConfigurationPatches(string(baseYAML), config)
}

// applyConfigurationPatches applies configuration patches to static YAML manifests
func (te *TemplateEngine) applyConfigurationPatches(baseYAML string, config *PlatformAppConfig) (string, error) {
	// For now, we'll create Kustomize patches and apply them
	// This is a transitional approach while we move to full KCL templating

	patches := te.generateKustomizePatches(config)

	// Create a temporary directory for Kustomize processing
	tempDir, err := os.MkdirTemp("", fmt.Sprintf("adhar-%s-", config.Name))
	if err != nil {
		return "", fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	// Write base manifest
	baseDir := filepath.Join(tempDir, "base")
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create base directory: %w", err)
	}

	manifestPath := filepath.Join(baseDir, "manifest.yaml")
	if err := os.WriteFile(manifestPath, []byte(baseYAML), 0644); err != nil {
		return "", fmt.Errorf("failed to write base manifest: %w", err)
	}

	// Create base kustomization
	baseKustomization := `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - manifest.yaml`

	if err := os.WriteFile(filepath.Join(baseDir, "kustomization.yaml"), []byte(baseKustomization), 0644); err != nil {
		return "", fmt.Errorf("failed to write base kustomization: %w", err)
	}

	// Create overlay with patches
	overlayDir := filepath.Join(tempDir, "overlay")
	if err := os.MkdirAll(overlayDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create overlay directory: %w", err)
	}

	overlayKustomization := fmt.Sprintf(`apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ../base

%s`, patches)

	if err := os.WriteFile(filepath.Join(overlayDir, "kustomization.yaml"), []byte(overlayKustomization), 0644); err != nil {
		return "", fmt.Errorf("failed to write overlay kustomization: %w", err)
	}

	// Generate final manifests using Kustomize
	cmd := exec.Command("kustomize", "build", overlayDir)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to build Kustomize overlay: %w", err)
	}

	return string(output), nil
}

// generateKustomizePatches generates Kustomize patches based on configuration
func (te *TemplateEngine) generateKustomizePatches(config *PlatformAppConfig) string {
	var patches strings.Builder

	patches.WriteString("patches:\n")

	// Generate patches based on the app type and configuration
	switch config.Name {
	case "gitea":
		patches.WriteString(te.generateGiteaPatches(config))
	case "argocd":
		patches.WriteString(te.generateArgoCDPatches(config))
	case "nginx":
		patches.WriteString(te.generateNginxPatches(config))
	case "cilium":
		patches.WriteString(te.generateCiliumPatches(config))
	case "crossplane":
		patches.WriteString(te.generateCrossplanePatches(config))
	}

	return patches.String()
}

// generateGiteaPatches generates Gitea-specific patches
func (te *TemplateEngine) generateGiteaPatches(config *PlatformAppConfig) string {
	return fmt.Sprintf(`  - target:
      kind: Deployment
      name: gitea
    patch: |-
      - op: replace
        path: /spec/replicas
        value: %d
      - op: add
        path: /spec/template/spec/containers/0/resources
        value:
          requests:
            cpu: "%s"
            memory: "%s"
          limits:
            cpu: "%s"
            memory: "%s"
  - target:
      kind: Deployment
      name: gitea-postgresql
    patch: |-
      - op: replace
        path: /spec/replicas
        value: %d
      - op: add
        path: /spec/template/spec/containers/0/resources
        value:
          requests:
            cpu: "%s"
            memory: "%s"
          limits:
            cpu: "%s"
            memory: "%s"
`,
		config.Replicas,
		config.Resources.CPURequest, config.Resources.MemoryRequest,
		config.Resources.CPULimit, config.Resources.MemoryLimit,
		config.PostgreSQLReplicas,
		config.Resources.CPURequest, config.Resources.MemoryRequest,
		config.Resources.CPULimit, config.Resources.MemoryLimit,
	)
}

// generateArgoCDPatches generates ArgoCD-specific patches
func (te *TemplateEngine) generateArgoCDPatches(config *PlatformAppConfig) string {
	return fmt.Sprintf(`  - target:
      kind: Deployment
      name: argocd-server
    patch: |-
      - op: replace
        path: /spec/replicas
        value: %d
      - op: add
        path: /spec/template/spec/containers/0/resources
        value:
          requests:
            cpu: "%s"
            memory: "%s"
          limits:
            cpu: "%s"
            memory: "%s"
  - target:
      kind: Deployment
      name: argocd-application-controller
    patch: |-
      - op: replace
        path: /spec/replicas
        value: %d
      - op: add
        path: /spec/template/spec/containers/0/resources
        value:
          requests:
            cpu: "%s"
            memory: "%s"
          limits:
            cpu: "%s"
            memory: "%s"
`,
		config.ServerReplicas,
		config.Resources.CPURequest, config.Resources.MemoryRequest,
		config.Resources.CPULimit, config.Resources.MemoryLimit,
		config.ControllerReplicas,
		config.Resources.CPURequest, config.Resources.MemoryRequest,
		config.Resources.CPULimit, config.Resources.MemoryLimit,
	)
}

// generateNginxPatches generates Nginx-specific patches
func (te *TemplateEngine) generateNginxPatches(config *PlatformAppConfig) string {
	return fmt.Sprintf(`  - target:
      kind: Deployment
      name: ingress-nginx-controller
    patch: |-
      - op: replace
        path: /spec/replicas
        value: %d
      - op: add
        path: /spec/template/spec/containers/0/resources
        value:
          requests:
            cpu: "%s"
            memory: "%s"
          limits:
            cpu: "%s"
            memory: "%s"
`,
		config.Replicas,
		config.Resources.CPURequest, config.Resources.MemoryRequest,
		config.Resources.CPULimit, config.Resources.MemoryLimit,
	)
}

// generateCiliumPatches generates Cilium-specific patches
func (te *TemplateEngine) generateCiliumPatches(config *PlatformAppConfig) string {
	patches := fmt.Sprintf(`  - target:
      kind: Deployment
      name: cilium-operator
    patch: |-
      - op: replace
        path: /spec/replicas
        value: %d
  - target:
      kind: DaemonSet
      name: cilium
    patch: |-
      - op: add
        path: /spec/template/spec/containers/0/resources
        value:
          requests:
            cpu: "%s"
            memory: "%s"
          limits:
            cpu: "%s"
            memory: "%s"
  - target:
      kind: ConfigMap
      name: cilium-config
    patch: |-
      - op: add
        path: /data/enable-wireguard
        value: "%t"
      - op: add
        path: /data/enable-l7-proxy
        value: "%t"
`,
		config.OperatorReplicas,
		config.Resources.CPURequest, config.Resources.MemoryRequest,
		config.Resources.CPULimit, config.Resources.MemoryLimit,
		config.EnableEncryption,
		config.EnableL7Proxy,
	)

	if !config.EnableEncryption {
		// Add patch to remove Hubble UI Ingress annotation for local mode
		patches += `  - target:
      kind: Ingress
      name: hubble-ui
    patch: |-
      - op: remove
        path: /metadata/annotations/kubernetes.io~1ingress.class
`
	}

	return patches
}

// generateCrossplanePatches generates Crossplane-specific patches
func (te *TemplateEngine) generateCrossplanePatches(config *PlatformAppConfig) string {
	return fmt.Sprintf(`  - target:
      kind: Deployment
      name: crossplane
    patch: |-
      - op: replace
        path: /spec/replicas
        value: %d
      - op: add
        path: /spec/template/spec/containers/0/resources
        value:
          requests:
            cpu: "%s"
            memory: "%s"
          limits:
            cpu: "%s"
            memory: "%s"
`,
		config.Replicas,
		config.Resources.CPURequest, config.Resources.MemoryRequest,
		config.Resources.CPULimit, config.Resources.MemoryLimit,
	)
}
