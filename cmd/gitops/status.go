package gitops

import (
	"fmt"

	"github.com/spf13/cobra"

	"adhar-io/adhar/cmd/apps"
	"adhar-io/adhar/platform/logger"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show GitOps status",
	Long: `Show GitOps status and application health.
	
Examples:
  adhar gitops status
  adhar gitops status --app=my-app
  adhar gitops status --namespace=prod`,
	RunE: runStatus,
}

func runStatus(cmd *cobra.Command, args []string) error {
	logger.Info("📊 Checking GitOps status...")

	if app != "" {
		return showApplicationStatus(cmd, app)
	}

	if namespace != "" {
		return showNamespaceStatus(namespace)
	}

	return showOverallStatus()
}

func showApplicationStatus(cmd *cobra.Command, appName string) error {
	logger.Info(fmt.Sprintf("📊 Showing status for application: %s", appName))

	kubeconfigPath, err := cmd.Root().PersistentFlags().GetString("kubeconfig")
	if err != nil {
		return fmt.Errorf("read kubeconfig flag: %w", err)
	}

	statusView, err := apps.GetApplicationStatus(cmd.Context(), kubeconfigPath, namespace, appName)
	if err != nil {
		return err
	}

	return apps.RenderApplicationStatus(statusView, output, true)
}

func showNamespaceStatus(namespaceName string) error {
	logger.Info(fmt.Sprintf("📊 Showing status for namespace: %s", namespaceName))

	// TODO: Implement namespace status display
	// This should show:
	// - All applications in namespace
	// - Overall health
	// - Sync status summary
	// - Issues and warnings

	logger.Info("✅ Namespace status displayed")
	return nil
}

func showOverallStatus() error {
	logger.Info("📊 Showing overall GitOps status...")

	// TODO: Implement overall status display
	// This should show:
	// - All applications
	// - Overall health score
	// - Sync status summary
	// - Repository connectivity
	// - ArgoCD server status

	logger.Info("✅ Overall status displayed")
	return nil
}
