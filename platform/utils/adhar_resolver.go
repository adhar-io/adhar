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

package utils

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// AdharResolver handles resolution of adhar:// shorthand references
type AdharResolver struct {
	BasePath string
	RepoURL  string
}

// NewAdharResolver creates a new AdharResolver instance
func NewAdharResolver(basePath, repoURL string) *AdharResolver {
	return &AdharResolver{
		BasePath: basePath,
		RepoURL:  repoURL,
	}
}

// ResolveAdharReferences resolves all adhar:// references in a YAML content
func (r *AdharResolver) ResolveAdharReferences(content string) (string, error) {
	// Pattern to match adhar:// references
	// Examples: adhar://packages/security/cert-manager, adhar://core/adhar-console
	pattern := regexp.MustCompile(`adhar://([a-zA-Z0-9\-_/]+)`)

	return pattern.ReplaceAllStringFunc(content, func(match string) string {
		// Extract the path after adhar://
		path := strings.TrimPrefix(match, "adhar://")

		// Resolve the path to full repository URL
		resolvedPath := r.resolvePath(path)

		return resolvedPath
	}), nil
}

// resolvePath resolves a shorthand path to a full repository path
func (r *AdharResolver) resolvePath(path string) string {
	// Handle different types of adhar:// references

	// 1. Package references: adhar://packages/security/cert-manager
	if strings.HasPrefix(path, "packages/") {
		return fmt.Sprintf("%s/%s", r.RepoURL, path)
	}

	// 2. Core references: adhar://core/adhar-console
	if strings.HasPrefix(path, "core/") {
		return fmt.Sprintf("%s/packages/%s", r.RepoURL, path)
	}

	// 3. Application references: adhar://application/argo-workflows
	if strings.HasPrefix(path, "application/") {
		return fmt.Sprintf("%s/packages/%s", r.RepoURL, path)
	}

	// 4. Infrastructure references: adhar://infrastructure/crossplane
	if strings.HasPrefix(path, "infrastructure/") {
		return fmt.Sprintf("%s/packages/%s", r.RepoURL, path)
	}

	// 5. Data references: adhar://data/postgresql
	if strings.HasPrefix(path, "data/") {
		return fmt.Sprintf("%s/packages/%s", r.RepoURL, path)
	}

	// 6. Security references: adhar://security/vault
	if strings.HasPrefix(path, "security/") {
		return fmt.Sprintf("%s/packages/%s", r.RepoURL, path)
	}

	// 7. Observability references: adhar://observability/prometheus
	if strings.HasPrefix(path, "observability/") {
		return fmt.Sprintf("%s/packages/%s", r.RepoURL, path)
	}

	// 8. Environment references: adhar://environments/local
	if strings.HasPrefix(path, "environments/") {
		// For environments, use the environments repository
		envRepoURL := strings.Replace(r.RepoURL, "packages", "environments", 1)
		return fmt.Sprintf("%s/%s", envRepoURL, path)
	}

	// Default: treat as packages path
	return fmt.Sprintf("%s/packages/%s", r.RepoURL, path)
}

// ResolveManifestPath resolves a manifest path within the repository
func (r *AdharResolver) ResolveManifestPath(componentPath, manifestType string) string {
	// Common manifest types and their default paths
	manifestPaths := map[string]string{
		"install":   "manifests/install.yaml",
		"values":    "values.yaml",
		"kustomize": "manifests/kustomization.yaml",
		"helm":      "Chart.yaml",
		"base":      "manifests/base",
		"dev":       "manifests/dev",
		"prod":      "manifests/prod",
		"staging":   "manifests/staging",
		"testing":   "manifests/testing",
	}

	if manifestPath, exists := manifestPaths[manifestType]; exists {
		return filepath.Join(componentPath, manifestPath)
	}

	// Default to manifests directory
	return filepath.Join(componentPath, "manifests")
}

// GetComponentPath returns the full path for a component
func (r *AdharResolver) GetComponentPath(component string) string {
	// Map common component names to their paths
	componentMap := map[string]string{
		"cert-manager":     "packages/security/cert-manager",
		"external-secrets": "packages/security/external-secrets",
		"vault":            "packages/security/vault",
		"keycloak":         "packages/security/keycloak",
		"kyverno":          "packages/security/kyverno",
		"postgresql":       "packages/data/postgresql",
		"redis":            "packages/data/redis",
		"minio":            "packages/data/minio",
		"kafka":            "packages/data/kafka",
		"prometheus":       "packages/observability/kube-prometheus",
		"grafana":          "packages/observability/kube-prometheus",
		"loki":             "packages/observability/loki-stack",
		"jaeger":           "packages/observability/jaeger",
		"argo-workflows":   "packages/application/argo-workflows",
		"argo-rollouts":    "packages/application/argo-rollout",
		"harbor":           "packages/application/harbor",
		"crossplane":       "packages/infrastructure/crossplane",
		"terraform":        "packages/infrastructure/terraform",
		"adhar-console":    "packages/core/adhar-console",
		"velero":           "packages/core/velero",
	}

	if path, exists := componentMap[component]; exists {
		return path
	}

	// Default: assume it's a package name
	return fmt.Sprintf("packages/%s", component)
}

// ResolveTemplateVariables resolves template variables in content
func (r *AdharResolver) ResolveTemplateVariables(content string, variables map[string]string) string {
	result := content

	for key, value := range variables {
		placeholder := fmt.Sprintf("{{ .%s }}", key)
		result = strings.ReplaceAll(result, placeholder, value)
	}

	return result
}

// ValidateAdharReference validates if an adhar:// reference is valid
func (r *AdharResolver) ValidateAdharReference(reference string) error {
	if !strings.HasPrefix(reference, "adhar://") {
		return fmt.Errorf("invalid adhar reference: %s (must start with adhar://)", reference)
	}

	path := strings.TrimPrefix(reference, "adhar://")
	if path == "" {
		return fmt.Errorf("invalid adhar reference: %s (path cannot be empty)", reference)
	}

	// Check if path contains valid characters
	validPattern := regexp.MustCompile(`^[a-zA-Z0-9\-_/]+$`)
	if !validPattern.MatchString(path) {
		return fmt.Errorf("invalid adhar reference: %s (path contains invalid characters)", reference)
	}

	return nil
}
