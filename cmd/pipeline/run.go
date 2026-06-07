package pipeline

import (
	"context"
	"fmt"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Run pipeline",
	Long: `Run a pipeline by submitting an Argo Workflow from a WorkflowTemplate.

The --name flag references an existing WorkflowTemplate in the namespace; a new
Workflow is created from it.

Examples:
  adhar pipeline run --name=deploy
  adhar pipeline run --name=build --namespace=argo`,
	RunE: runRun,
}

func runRun(cmd *cobra.Command, args []string) error {
	if pipelineName == "" {
		return fmt.Errorf("--name is required for running pipeline (references a WorkflowTemplate)")
	}

	ns := defaultNamespace()
	logger.Info(fmt.Sprintf("🚀 Running pipeline from template: %s", pipelineName))

	client, err := getDynamicClient()
	if err != nil {
		return err
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	// Verify the referenced WorkflowTemplate exists for a friendly error.
	if _, err := client.Resource(workflowTemplatesGVR).Namespace(ns).Get(ctx, pipelineName, metav1.GetOptions{}); err != nil {
		if k8serrors.IsNotFound(err) {
			return fmt.Errorf("workflow template %q not found in namespace %q", pipelineName, ns)
		}
		return fmt.Errorf("get workflow template: %w", err)
	}

	wf := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Workflow",
		"metadata": map[string]interface{}{
			"generateName": pipelineName + "-",
			"namespace":    ns,
		},
		"spec": map[string]interface{}{
			"workflowTemplateRef": map[string]interface{}{
				"name": pipelineName,
			},
		},
	}}

	created, err := client.Resource(workflowsGVR).Namespace(ns).Create(ctx, wf, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("create workflow: %w", err)
	}

	fmt.Println(helpers.CreateSuccess(fmt.Sprintf("Pipeline started: workflow %s in namespace %s", created.GetName(), ns)))
	return nil
}
