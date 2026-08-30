package gitops

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

var (
	syncApp       string
	syncNamespace string
)

var syncCmd = &cobra.Command{
	Use:   "sync [app]",
	Short: "Sync applications",
	Long: `Trigger an ArgoCD sync for one or all Applications.

The sync is requested through the control plane by refreshing the Application and
setting its operation — the same path the ArgoCD UI / Console uses.

Examples:
  adhar gitops sync
  adhar gitops sync my-app
  adhar gitops sync --app my-app --revision main --prune`,
	Args: cobra.MaximumNArgs(1),
	RunE: runSync,
}

func init() {
	syncCmd.Flags().StringVarP(&syncApp, "app", "a", "", "Application name (defaults to all)")
	syncCmd.Flags().StringVarP(&syncNamespace, "namespace", "n", argoNamespace, "ArgoCD namespace")
	syncCmd.Flags().StringVar(&revision, "revision", "", "Git revision/branch/tag to sync to")
	syncCmd.Flags().BoolVar(&prune, "prune", false, "Prune resources not tracked in Git")
}

func runSync(cmd *cobra.Command, args []string) error {
	name := syncApp
	if name == "" && len(args) > 0 {
		name = args[0]
	}

	client, err := helpers.DynamicClient()
	if err != nil {
		return err
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	if name != "" {
		return syncApplication(ctx, client, name)
	}
	return syncAllApplications(ctx, client)
}

// syncOperationPatch builds a strategic/merge patch that refreshes the Application
// and requests a sync operation. ArgoCD watches `.operation` and executes it.
func syncOperationPatch() ([]byte, error) {
	syncBody := map[string]interface{}{
		"prune": prune,
		"syncStrategy": map[string]interface{}{
			"hook": map[string]interface{}{},
		},
	}
	if revision != "" {
		syncBody["revision"] = revision
	}
	patch := map[string]interface{}{
		"metadata": map[string]interface{}{
			"annotations": map[string]interface{}{
				"argocd.argoproj.io/refresh": "hard",
			},
		},
		"operation": map[string]interface{}{
			"initiatedBy": map[string]interface{}{"username": "adhar-cli"},
			"sync":        syncBody,
		},
	}
	return json.Marshal(patch)
}

func syncApplication(ctx context.Context, client dynamic.Interface, appName string) error {
	logger.Info(fmt.Sprintf("🔄 Syncing application: %s", appName))

	patch, err := syncOperationPatch()
	if err != nil {
		return fmt.Errorf("build sync patch: %w", err)
	}

	_, err = client.Resource(applicationsGVR).Namespace(syncNamespace).
		Patch(ctx, appName, types.MergePatchType, patch, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("sync application %q: %w", appName, err)
	}

	fmt.Println(helpers.CreateSuccess(fmt.Sprintf("Sync requested for application %s", appName)))
	return nil
}

func syncAllApplications(ctx context.Context, client dynamic.Interface) error {
	logger.Info("🔄 Syncing all applications...")

	list, err := client.Resource(applicationsGVR).Namespace(syncNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list applications: %w", err)
	}
	if len(list.Items) == 0 {
		fmt.Println(helpers.InfoStyle.Render("No ArgoCD applications found."))
		return nil
	}

	patch, err := syncOperationPatch()
	if err != nil {
		return fmt.Errorf("build sync patch: %w", err)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "APPLICATION\tRESULT")
	for _, item := range list.Items {
		appName := item.GetName()
		_, perr := client.Resource(applicationsGVR).Namespace(syncNamespace).
			Patch(ctx, appName, types.MergePatchType, patch, metav1.PatchOptions{})
		result := "sync requested"
		if perr != nil {
			result = "error: " + perr.Error()
		}
		fmt.Fprintf(w, "%s\t%s\n", appName, result)
	}
	return w.Flush()
}
