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

var templateCmd = &cobra.Command{
	Use:   "template",
	Short: "Manage pipeline templates",
	Long: `Manage pipeline templates (Argo WorkflowTemplates).

Examples:
  adhar pipeline template list
  adhar pipeline template list --namespace=argo`,
	RunE: runTemplate,
}

var templateListCmd = &cobra.Command{
	Use:   "list",
	Short: "List pipeline templates",
	Long: `List Argo WorkflowTemplates available in the target namespace.

Examples:
  adhar pipeline template list`,
	RunE: runTemplateList,
}

func init() {
	templateCmd.AddCommand(templateListCmd)
}

func runTemplate(cmd *cobra.Command, args []string) error {
	return cmd.Help()
}

func runTemplateList(cmd *cobra.Command, args []string) error {
	ns := defaultNamespace()
	logger.Info(fmt.Sprintf("📋 Listing pipeline templates in namespace %s...", ns))

	client, err := getDynamicClient()
	if err != nil {
		return err
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	tplList, err := client.Resource(workflowTemplatesGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list workflow templates: %w", err)
	}

	if len(tplList.Items) == 0 {
		fmt.Println(helpers.InfoStyle.Render("No pipeline templates (workflow templates) found."))
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tNAMESPACE\tENTRYPOINT\tCREATED")
	for _, item := range tplList.Items {
		obj := item.Object
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
			stringField(obj, "metadata", "name"),
			stringField(obj, "metadata", "namespace"),
			valueOrDash(stringField(obj, "spec", "entrypoint")),
			valueOrDash(stringField(obj, "metadata", "creationTimestamp")),
		)
	}
	return w.Flush()
}
