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

package gitops

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"adhar-io/adhar/platform/logger"
	"adhar-io/adhar/platform/stack"
	"adhar-io/adhar/platform/utils"

	"github.com/spf13/cobra"
)

// resolveCmd represents the resolve command
var resolveCmd = &cobra.Command{
	Use:   "resolve [input-file] [output-file]",
	Short: "Resolve adhar:// references in GitOps manifests",
	Long: `Resolve adhar:// shorthand references in GitOps manifests to their full repository URLs.

This command processes YAML files containing adhar:// references and resolves them to their
corresponding full repository paths within the internal Gitea repositories.

Examples:
  # Resolve references in a single file
  adhar gitops resolve input.yaml output.yaml

  # Resolve references in ApplicationSet
  adhar gitops resolve platform/stack/adhar-appset-gitops.yaml resolved-appset.yaml

  # Validate all adhar:// references in platform stack
  adhar gitops resolve --validate-only`,
	Args: cobra.RangeArgs(0, 2),
	RunE: runResolve,
}

var (
	validateOnly     bool
	packagesRepo     string
	environmentsRepo string
)

func init() {
	GitOpsCmd.AddCommand(resolveCmd)

	resolveCmd.Flags().BoolVar(&validateOnly, "validate-only", false, "Only validate adhar:// references without resolving")
	resolveCmd.Flags().StringVar(&packagesRepo, "packages-repo", "http://gitea-argocd.adhar-system.svc.cluster.local:3000/gitea_admin/packages", "Packages repository URL")
	resolveCmd.Flags().StringVar(&environmentsRepo, "environments-repo", "http://gitea-argocd.adhar-system.svc.cluster.local:3000/gitea_admin/environments", "Environments repository URL")
}

func runResolve(cmd *cobra.Command, args []string) error {
	logger.Info("🔍 Resolving adhar:// references in GitOps manifests")

	// Create GitOps resolver
	resolver := stack.NewGitOpsResolver()

	if validateOnly {
		// Only validate references
		if err := resolver.ValidateAdharReferences(); err != nil {
			return fmt.Errorf("validation failed: %w", err)
		}
		logger.Info("✅ All adhar:// references are valid")
		return nil
	}

	if len(args) == 0 {
		// Generate all GitOps manifests
		if err := resolver.GenerateGitOpsManifests(); err != nil {
			return fmt.Errorf("failed to generate GitOps manifests: %w", err)
		}
		logger.Info("✅ Successfully generated all GitOps manifests")
		return nil
	}

	if len(args) < 2 {
		return fmt.Errorf("input and output files are required for resolution")
	}

	inputFile := args[0]
	outputFile := args[1]

	// Validate input file exists
	if _, err := os.Stat(inputFile); os.IsNotExist(err) {
		return fmt.Errorf("input file does not exist: %s", inputFile)
	}

	// Create output directory if it doesn't exist
	outputDir := filepath.Dir(outputFile)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	// Process the file
	if err := resolver.ProcessApplicationSetWithAdharReferences(inputFile, outputFile); err != nil {
		return fmt.Errorf("failed to process file: %w", err)
	}

	logger.Infof("✅ Successfully resolved adhar:// references: %s -> %s", inputFile, outputFile)
	return nil
}

// ResolveAdharReferencesInContent resolves adhar:// references in YAML content
func ResolveAdharReferencesInContent(content string, packagesRepo, environmentsRepo string) (string, error) {
	// Create adhar resolver
	adharResolver := utils.NewAdharResolver("platform/stack", packagesRepo)

	// Resolve adhar:// references
	resolvedContent, err := adharResolver.ResolveAdharReferences(content)
	if err != nil {
		return "", fmt.Errorf("failed to resolve adhar:// references: %w", err)
	}

	return resolvedContent, nil
}

// ValidateAdharReferencesInContent validates adhar:// references in YAML content
func ValidateAdharReferencesInContent(content string, packagesRepo, environmentsRepo string) error {
	// Create adhar resolver
	adharResolver := utils.NewAdharResolver("platform/stack", packagesRepo)

	// Check for adhar:// references
	lines := strings.Split(content, "\n")
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
					return fmt.Errorf("invalid adhar:// reference in line %d: %w", lineNum+1, err)
				}
			}
		}
	}

	return nil
}
