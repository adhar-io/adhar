package pipeline

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

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List all pipelines",
	Long: `List Argo Workflows (pipelines) in the target namespace.

Examples:
  adhar pipeline list
  adhar pipeline list --namespace=argo`,
	RunE: runList,
}

func runList(cmd *cobra.Command, args []string) error {
	ns := defaultNamespace()
	logger.Info(fmt.Sprintf("📋 Listing pipelines in namespace %s...", ns))

	client, err := getDynamicClient()
	if err != nil {
		return err
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	wfList, err := client.Resource(workflowsGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list workflows: %w", err)
	}

	if len(wfList.Items) == 0 {
		fmt.Println(helpers.InfoStyle.Render("No pipelines (workflows) found."))
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tNAMESPACE\tPHASE\tSTARTED\tFINISHED")
	for _, item := range wfList.Items {
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

func valueOrDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
