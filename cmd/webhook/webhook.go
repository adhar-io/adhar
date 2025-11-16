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

package webhook

import (
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
)

// WebhookCmd represents the webhook command
var WebhookCmd = &cobra.Command{
	Use:   "webhook",
	Short: "Manage webhooks and integrations",
	Long: `Manage webhooks, integrations, and external service connections.
	
This command provides:
• Webhook creation and configuration
• Integration management with external services
• Webhook testing and validation
• Security and authentication setup
• Event routing and filtering
• Webhook monitoring and logging

Examples:
  adhar webhook list                    # List all webhooks
  adhar webhook create --name=github    # Create new webhook
  adhar webhook test --name=github     # Test webhook
  adhar webhook monitor --name=github  # Monitor webhook activity`,
	RunE: runWebhook,
}

var (
	// Webhook command flags
	webhookName string
	webhookURL  string
	webhookType string
	namespace   string
	service     string
	timeout     string
	output      string
	detailed    bool
)

func init() {
	// Webhook command flags
	WebhookCmd.Flags().StringVarP(&webhookName, "name", "n", "", "Webhook name")
	WebhookCmd.Flags().StringVarP(&webhookURL, "url", "u", "", "Webhook URL")
	WebhookCmd.Flags().StringVarP(&webhookType, "type", "t", "", "Webhook type (github, slack, custom)")
	WebhookCmd.Flags().StringVarP(&namespace, "namespace", "s", "", "Namespace")
	WebhookCmd.Flags().StringVar(&service, "service", "", "Service name")
	WebhookCmd.Flags().StringVarP(&timeout, "timeout", "o", "30s", "Operation timeout")
	WebhookCmd.Flags().StringVarP(&output, "output", "f", "", "Output format (table, json, yaml)")
	WebhookCmd.Flags().BoolVarP(&detailed, "detailed", "d", false, "Show detailed information")

	// Add subcommands
	WebhookCmd.AddCommand(listCmd)
	WebhookCmd.AddCommand(createCmd)
	WebhookCmd.AddCommand(testCmd)
	WebhookCmd.AddCommand(monitorCmd)
	WebhookCmd.AddCommand(securityCmd)
}

func runWebhook(cmd *cobra.Command, args []string) error {
	logger.Info("🔗 Webhook management - use subcommands for specific webhook tasks")
	logger.Info("Available subcommands:")
	logger.Info("  list     - List all webhooks")
	logger.Info("  create   - Create new webhooks")
	logger.Info("  test     - Test webhooks")
	logger.Info("  monitor  - Monitor webhook activity")
	logger.Info("  security - Manage webhook security")

	return cmd.Help()
}
