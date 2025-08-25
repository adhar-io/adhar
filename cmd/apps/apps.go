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

package apps

import (
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
)

// AppsCmd represents the apps command
var AppsCmd = &cobra.Command{
	Use:   "apps",
	Short: "Manage the application development lifecycle",
	Long: `Manage applications throughout their development lifecycle.
	
This command provides:
• Application deployment and management
• Template-based application creation
• Application lifecycle operations
• Monitoring and status tracking
• Scaling and configuration updates

Examples:
  adhar apps deploy my-app --template=nodejs
  adhar apps list
  adhar apps status my-app
  adhar apps scale my-app --replicas=3`,
	RunE: runApps,
}

var (
	// Global flags for apps command
	namespace string
	output    string
	verbose   bool
)

func init() {
	// Global flags
	AppsCmd.PersistentFlags().StringVarP(&namespace, "namespace", "n", "default", "Namespace for the application")
	AppsCmd.PersistentFlags().StringVarP(&output, "output", "o", "table", "Output format (table, json, yaml)")
	// Verbose flag is handled globally by root command

	// Add subcommands
	AppsCmd.AddCommand(deployCmd)
	AppsCmd.AddCommand(listCmd)
	AppsCmd.AddCommand(statusCmd)
	AppsCmd.AddCommand(scaleCmd)
	AppsCmd.AddCommand(deleteCmd)
}

func runApps(cmd *cobra.Command, args []string) error {
	logger.Info("📱 Apps command - use subcommands to manage applications")
	logger.Info("Available subcommands:")
	logger.Info("  deploy  - Deploy applications from templates or Git")
	logger.Info("  list    - List all applications")
	logger.Info("  status  - Check application status")
	logger.Info("  scale   - Scale applications")
	logger.Info("  delete  - Delete applications")

	return cmd.Help()
}
