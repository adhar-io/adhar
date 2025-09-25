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

package stack

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"adhar-io/adhar/platform/logger"
	"adhar-io/adhar/platform/utils"
)

// GitOpsResolver handles GitOps operations and adhar:// reference resolution
type GitOpsResolver struct {
	BasePath         string
	PackagesRepo     string
	EnvironmentsRepo string
}

// NewGitOpsResolver creates a new GitOpsResolver instance
func NewGitOpsResolver() *GitOpsResolver {
	return &GitOpsResolver{
		BasePath:         "platform/stack",
		PackagesRepo:     "http://gitea-argocd.adhar-system.svc.cluster.local:3000/gitea_admin/packages",
		EnvironmentsRepo: "http://gitea-argocd.adhar-system.svc.cluster.local:3000/gitea_admin/environments",
	}
}

// ProcessApplicationSetWithAdharReferences processes an ApplicationSet and resolves adhar:// references
func (r *GitOpsResolver) ProcessApplicationSetWithAdharReferences(inputFile, outputFile string) error {
	logger.Info("Processing ApplicationSet with adhar:// references")

	// Read the input ApplicationSet
	content, err := os.ReadFile(inputFile)
	if err != nil {
		return fmt.Errorf("failed to read ApplicationSet file: %w", err)
	}

	// Create adhar resolver
	adharResolver := utils.NewAdharResolver(r.BasePath, r.PackagesRepo)

	// Resolve adhar:// references
	resolvedContent, err := adharResolver.ResolveAdharReferences(string(content))
	if err != nil {
		return fmt.Errorf("failed to resolve adhar:// references: %w", err)
	}

	// Write the resolved content to output file
	if err := os.WriteFile(outputFile, []byte(resolvedContent), 0644); err != nil {
		return fmt.Errorf("failed to write resolved ApplicationSet: %w", err)
	}

	logger.Infof("Successfully processed ApplicationSet: %s -> %s", inputFile, outputFile)
	return nil
}

// GenerateGitOpsManifests generates GitOps manifests with resolved adhar:// references
func (r *GitOpsResolver) GenerateGitOpsManifests() error {
	logger.Info("Generating GitOps manifests with adhar:// references")

	// Process the GitOps ApplicationSet
	gitopsInput := filepath.Join(r.BasePath, "adhar-appset-gitops.yaml")
	gitopsOutput := filepath.Join(r.BasePath, "generated", "adhar-appset-gitops-resolved.yaml")

	// Create output directory
	if err := os.MkdirAll(filepath.Dir(gitopsOutput), 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	if err := r.ProcessApplicationSetWithAdharReferences(gitopsInput, gitopsOutput); err != nil {
		return fmt.Errorf("failed to process GitOps ApplicationSet: %w", err)
	}

	// Process the local ApplicationSet
	localInput := filepath.Join(r.BasePath, "adhar-appset-local.yaml")
	localOutput := filepath.Join(r.BasePath, "generated", "adhar-appset-local-resolved.yaml")

	if err := r.ProcessApplicationSetWithAdharReferences(localInput, localOutput); err != nil {
		return fmt.Errorf("failed to process local ApplicationSet: %w", err)
	}

	logger.Info("Successfully generated all GitOps manifests")
	return nil
}

// ValidateAdharReferences validates all adhar:// references in the platform stack
func (r *GitOpsResolver) ValidateAdharReferences() error {
	logger.Info("Validating adhar:// references in platform stack")

	adharResolver := utils.NewAdharResolver(r.BasePath, r.PackagesRepo)

	// Walk through all YAML files in the platform stack
	err := filepath.Walk(r.BasePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip directories and non-YAML files
		if info.IsDir() || !strings.HasSuffix(strings.ToLower(path), ".yaml") {
			return nil
		}

		// Read file content
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		// Check for adhar:// references
		contentStr := string(content)
		if strings.Contains(contentStr, "adhar://") {
			// Extract adhar:// references using regex
			lines := strings.Split(contentStr, "\n")
			for lineNum, line := range lines {
				if strings.Contains(line, "adhar://") {
					// Find the adhar:// reference
					start := strings.Index(line, "adhar://")
					if start != -1 {
						end := start
						for end < len(line) && line[end] != ' ' && line[end] != '\t' && line[end] != '\n' && line[end] != '\r' {
							end++
						}
						reference := line[start:end]

						// Validate the reference
						if err := adharResolver.ValidateAdharReference(reference); err != nil {
							return fmt.Errorf("invalid adhar:// reference in %s:%d: %w", path, lineNum+1, err)
						}

						logger.Debugf("Valid adhar:// reference found: %s in %s:%d", reference, path, lineNum+1)
					}
				}
			}
		}

		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to validate adhar:// references: %w", err)
	}

	logger.Info("All adhar:// references are valid")
	return nil
}

// CreateGitOpsApplicationSet creates a GitOps ApplicationSet with resolved references
func (r *GitOpsResolver) CreateGitOpsApplicationSet() error {
	logger.Info("Creating GitOps ApplicationSet with resolved references")

	// Generate manifests first
	if err := r.GenerateGitOpsManifests(); err != nil {
		return fmt.Errorf("failed to generate GitOps manifests: %w", err)
	}

	// Apply the resolved ApplicationSet
	resolvedAppSet := filepath.Join(r.BasePath, "generated", "adhar-appset-gitops-resolved.yaml")

	cmd := exec.Command("kubectl", "apply", "-f", resolvedAppSet)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to apply GitOps ApplicationSet: %w", err)
	}

	logger.Info("Successfully created GitOps ApplicationSet")
	return nil
}

// UpdateApplicationSetToUseGitOps updates the existing ApplicationSet to use GitOps
func (r *GitOpsResolver) UpdateApplicationSetToUseGitOps() error {
	logger.Info("Updating ApplicationSet to use GitOps with adhar:// references")

	// First, validate all references
	if err := r.ValidateAdharReferences(); err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}

	// Create the GitOps ApplicationSet
	if err := r.CreateGitOpsApplicationSet(); err != nil {
		return fmt.Errorf("failed to create GitOps ApplicationSet: %w", err)
	}

	logger.Info("Successfully updated ApplicationSet to use GitOps")
	return nil
}
