/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the file at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package gitops

import (
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
)

// GitOpsCmd represents the gitops command
var GitOpsCmd = &cobra.Command{
	Use:   "gitops",
	Short: "Manage GitOps operations and ArgoCD",
	Long: `Manage GitOps operations and ArgoCD for the Adhar platform.
	
This command provides:
• Application deployment and synchronization
• Git repository management and connectivity
• ArgoCD application lifecycle management
• Deployment rollbacks and history
• GitOps workflow automation
• Multi-environment deployment management

Examples:
  adhar gitops sync                    # Sync all applications
  adhar gitops sync --app=my-app      # Sync specific application
  adhar gitops rollback --app=my-app  # Rollback application
  adhar gitops status                 # Show GitOps status
  adhar gitops repo add --url=git://  # Add Git repository`,
	RunE: runGitOps,
}

var (
	// GitOps command flags
	app       string
	repo      string
	namespace string
	revision  string
	prune     bool
	force     bool
	timeout   string
	output    string
)

func init() {
	// GitOps command flags
	GitOpsCmd.Flags().StringVarP(&app, "app", "a", "", "Application name")
	GitOpsCmd.Flags().StringVarP(&repo, "repo", "r", "", "Repository URL")
	GitOpsCmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Namespace")
	GitOpsCmd.Flags().StringVarP(&revision, "revision", "", "", "Git revision/branch/tag")
	GitOpsCmd.Flags().BoolVar(&prune, "prune", false, "Prune resources")
	GitOpsCmd.Flags().BoolVar(&force, "force", false, "Force operation")
	GitOpsCmd.Flags().StringVarP(&timeout, "timeout", "i", "5m", "Operation timeout")
	GitOpsCmd.Flags().StringVarP(&output, "output", "f", "", "Output format (table, json, yaml)")

	// Add subcommands
	GitOpsCmd.AddCommand(syncCmd)
	GitOpsCmd.AddCommand(statusCmd)
	GitOpsCmd.AddCommand(rollbackCmd)
	GitOpsCmd.AddCommand(repoCmd)
	GitOpsCmd.AddCommand(workflowCmd)
}

func runGitOps(cmd *cobra.Command, args []string) error {
	logger.Info("🔄 GitOps management - use subcommands for specific GitOps tasks")
	logger.Info("Available subcommands:")
	logger.Info("  sync     - Sync applications")
	logger.Info("  status   - Show GitOps status")
	logger.Info("  rollback - Rollback deployments")
	logger.Info("  repo     - Manage Git repositories")
	logger.Info("  workflow - Manage GitOps workflows")

	return cmd.Help()
}
