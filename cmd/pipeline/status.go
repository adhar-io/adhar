package pipeline

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check pipeline status",
	Long: `Check the status (phase) of Argo Workflows.

Examples:
  adhar pipeline status --name=deploy
  adhar pipeline status`,
	RunE: runStatus,
}

func runStatus(cmd *cobra.Command, args []string) error {
	client, err := getDynamicClient()
	if err != nil {
		return err
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	if pipelineName != "" {
		return checkPipelineStatus(ctx, client, pipelineName)
	}
	return checkAllPipelinesStatus(ctx, client)
}

func checkPipelineStatus(ctx context.Context, client dynamic.Interface, name string) error {
	ns := defaultNamespace()
	logger.Info(fmt.Sprintf("📊 Checking status for pipeline: %s", name))

	wf, err := client.Resource(workflowsGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return fmt.Errorf("pipeline (workflow) %q not found in namespace %q", name, ns)
		}
		return fmt.Errorf("get workflow: %w", err)
	}

	obj := wf.Object
	builder := ""
	add := func(label, value string) {
		builder += fmt.Sprintf("%s %s\n", helpers.BulletStyle.Render(label), valueOrDash(value))
	}
	add("Pipeline:", stringField(obj, "metadata", "name"))
	add("Namespace:", stringField(obj, "metadata", "namespace"))
	add("Phase:", stringField(obj, "status", "phase"))
	add("Message:", stringField(obj, "status", "message"))
	add("Started:", stringField(obj, "status", "startedAt"))
	add("Finished:", stringField(obj, "status", "finishedAt"))
	fmt.Println(helpers.CreateBox(builder, 90))
	return nil
}

func checkAllPipelinesStatus(ctx context.Context, client dynamic.Interface) error {
	ns := defaultNamespace()
	logger.Info(fmt.Sprintf("📊 Checking status for all pipelines in namespace %s...", ns))

	wfList, err := client.Resource(workflowsGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list workflows: %w", err)
	}

	if len(wfList.Items) == 0 {
		fmt.Println(helpers.InfoStyle.Render("No pipelines (workflows) found."))
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tPHASE\tMESSAGE")
	for _, item := range wfList.Items {
		obj := item.Object
		fmt.Fprintf(w, "%s\t%s\t%s\n",
			stringField(obj, "metadata", "name"),
			valueOrDash(stringField(obj, "status", "phase")),
			valueOrDash(stringField(obj, "status", "message")),
		)
	}
	return w.Flush()
}
