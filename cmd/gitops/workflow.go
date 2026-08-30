package gitops

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var workflowNamespace string

var workflowCmd = &cobra.Command{
	Use:   "workflow",
	Short: "List GitOps workflows",
	Long: `List Argo Workflows (argoproj.io/v1alpha1) driving GitOps automation.

Examples:
  adhar gitops workflow
  adhar gitops workflow --namespace argo`,
	RunE: runWorkflow,
}

func init() {
	workflowCmd.Flags().StringVarP(&workflowNamespace, "namespace", "n", "argo", "Argo Workflows namespace")
}

func runWorkflow(cmd *cobra.Command, args []string) error {
	logger.Info(fmt.Sprintf("⚡ Listing Argo Workflows in namespace %s...", workflowNamespace))

	client, err := helpers.DynamicClient()
	if err != nil {
		return err
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	list, err := client.Resource(gitopsWorkflowsGVR).Namespace(workflowNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list workflows: %w", err)
	}

	if len(list.Items) == 0 {
		fmt.Println(helpers.InfoStyle.Render("No Argo Workflows found."))
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tNAMESPACE\tPHASE\tSTARTED\tFINISHED")
	for _, item := range list.Items {
		obj := item.Object
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			stringField(obj, "metadata", "name"),
			stringField(obj, "metadata", "namespace"),
			valueOrDash(stringField(obj, "status", "phase")),
			valueOrDash(stringField(obj, "status", "startedAt")),
			valueOrDash(stringField(obj, "status", "finishedAt")),
		)
	}
	return w.Flush()
}
