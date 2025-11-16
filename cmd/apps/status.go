package apps

import (
	"fmt"

	"github.com/spf13/cobra"

	"adhar-io/adhar/platform/logger"
)

// statusCmd represents the status command
var statusCmd = &cobra.Command{
	Use:   "status [app-name]",
	Short: "Check application status",
	Long: `Check the status of a specific application.
	
Examples:
  adhar apps status my-app
  adhar apps status my-app --detailed`,
	Args: cobra.ExactArgs(1),
	RunE: runStatus,
}

var (
	// Status-specific flags
	detailed bool
)

func init() {
	statusCmd.Flags().BoolVarP(&detailed, "detailed", "d", false, "Show detailed status information")
}

func runStatus(cmd *cobra.Command, args []string) error {
	appName := args[0]
	logger.Info(fmt.Sprintf("📊 Checking status for application: %s", appName))

	kubeconfigPath, err := cmd.Root().PersistentFlags().GetString("kubeconfig")
	if err != nil {
		return fmt.Errorf("read kubeconfig flag: %w", err)
	}

	status, err := GetApplicationStatus(cmd.Context(), kubeconfigPath, namespace, appName)
	if err != nil {
		return err
	}

	return RenderApplicationStatus(status, output, detailed)
}
