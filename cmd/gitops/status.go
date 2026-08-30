package gitops

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"adhar-io/adhar/cmd/apps"
	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"
)

var (
	statusApp       string
	statusNamespace string
	statusOutput    string
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show GitOps status",
	Long: `Show ArgoCD Application sync and health status.

With no --app, lists every Application in the namespace with its sync and health
status (read from the live ArgoCD Applications via the control plane).

Examples:
  adhar gitops status
  adhar gitops status --app my-app
  adhar gitops status --namespace adhar-system`,
	RunE: runStatus,
}

func init() {
	statusCmd.Flags().StringVarP(&statusApp, "app", "a", "", "Application name")
	statusCmd.Flags().StringVarP(&statusNamespace, "namespace", "n", argoNamespace, "ArgoCD namespace")
	statusCmd.Flags().StringVarP(&statusOutput, "output", "o", "", "Output format (table, json, yaml)")
}

func runStatus(cmd *cobra.Command, args []string) error {
	if statusApp != "" {
		return showApplicationStatus(cmd, statusApp)
	}
	return listApplicationStatus(cmd)
}

// showApplicationStatus renders the detailed status of a single Application via
// the shared apps helper (same view the `adhar apps` commands use).
func showApplicationStatus(cmd *cobra.Command, appName string) error {
	logger.Info(fmt.Sprintf("📊 Showing status for application: %s", appName))

	kubeconfigPath, err := cmd.Root().PersistentFlags().GetString("kubeconfig")
	if err != nil {
		return fmt.Errorf("read kubeconfig flag: %w", err)
	}

	statusView, err := apps.GetApplicationStatus(cmd.Context(), kubeconfigPath, statusNamespace, appName)
	if err != nil {
		return err
	}

	return apps.RenderApplicationStatus(statusView, statusOutput, true)
}

// listApplicationStatus lists all ArgoCD Applications with sync + health status.
func listApplicationStatus(cmd *cobra.Command) error {
	logger.Info(fmt.Sprintf("📊 Listing ArgoCD applications in namespace %s...", statusNamespace))

	client, err := helpers.DynamicClient()
	if err != nil {
		return err
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	list, err := client.Resource(applicationsGVR).Namespace(statusNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list applications: %w", err)
	}

	if len(list.Items) == 0 {
		fmt.Println(helpers.InfoStyle.Render("No ArgoCD applications found."))
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tNAMESPACE\tSYNC\tHEALTH\tREVISION")
	for _, item := range list.Items {
		obj := item.Object
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			stringField(obj, "metadata", "name"),
			stringField(obj, "metadata", "namespace"),
			valueOrDash(stringField(obj, "status", "sync", "status")),
			valueOrDash(stringField(obj, "status", "health", "status")),
			valueOrDash(stringField(obj, "spec", "source", "targetRevision")),
		)
	}
	return w.Flush()
}
