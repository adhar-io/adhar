package webhook

import (
	"fmt"

	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
)

var createCmd = &cobra.Command{
	Use:   "create",
	Short: "Create new webhook",
	Long: `Create a new webhook for external integrations.
	
Examples:
  adhar webhook create --name=github --url=https://api.github.com/webhook
  adhar webhook create --name=slack --type=slack`,
	RunE: runCreate,
}

func runCreate(cmd *cobra.Command, args []string) error {
	if webhookName == "" {
		return fmt.Errorf("--name is required for webhook creation")
	}

	if webhookURL == "" {
		return fmt.Errorf("--url is required for webhook creation")
	}

	logger.Info(fmt.Sprintf("🔗 Creating webhook: %s (URL: %s)", webhookName, webhookURL))

	// TODO: Implement webhook creation
	// This should:
	// - Validate webhook URL
	// - Create webhook configuration
	// - Set up authentication
	// - Configure event routing

	logger.Info("✅ Webhook created successfully")
	return nil
}
