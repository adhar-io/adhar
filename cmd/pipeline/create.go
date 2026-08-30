package pipeline

import (
	"context"
	"fmt"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
)

var createNamespace string

var createCmd = &cobra.Command{
	Use:   "create",
	Short: "Create new pipeline",
	Long: `Create a new CI/CD pipeline as a Crossplane CompositePipeline.

The control plane composes the pipeline into the backing CI/CD engine — locally an
Argo Workflow — the same provider-aware path the Adhar Console uses.

Examples:
  adhar pipeline create --name=deploy --type=deploy
  adhar pipeline create --name=build --type=build --namespace=argo`,
	RunE: runCreate,
}

func init() {
	createCmd.Flags().StringVarP(&pipelineName, "name", "n", "", "Pipeline name (required)")
	createCmd.Flags().StringVarP(&pipelineType, "type", "t", "", "Pipeline type: build, deploy, test, release, integration (required)")
	createCmd.Flags().StringVarP(&createNamespace, "namespace", "s", "default", "Namespace for pipeline execution")
}

func runCreate(cmd *cobra.Command, args []string) error {
	if pipelineName == "" {
		return fmt.Errorf("--name is required for pipeline creation")
	}
	if pipelineType == "" {
		return fmt.Errorf("--type is required for pipeline creation")
	}
	if !validPipelineType(pipelineType) {
		return fmt.Errorf("invalid pipeline type %q (allowed: build, deploy, test, release, integration)", pipelineType)
	}

	ns := createNamespace
	if ns == "" {
		ns = "default"
	}

	logger.Info(fmt.Sprintf("🔧 Creating pipeline: %s (type: %s, provider: %s)",
		pipelineName, pipelineType, helpers.ActiveProvider()))

	// Flat CompositePipeline spec (matches xrd/pipeline.xrd.yaml — spec.name and
	// spec.type are required). NewXR merges the provider-aware compositionSelector
	// under spec.crossplane so the CLI selects exactly the way the Console does.
	spec := map[string]interface{}{
		"name":      pipelineName,
		"type":      pipelineType,
		"namespace": ns,
	}

	xr := helpers.NewXR("CompositePipeline", pipelineName, ns, "pipeline", nil, spec)

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if err := helpers.ApplyXR(ctx, "compositepipelines", xr); err != nil {
		return fmt.Errorf("create pipeline: %w", err)
	}

	fmt.Println(helpers.CreateSuccess(fmt.Sprintf(
		"Pipeline %s (%s) created in namespace %s — composing via the control plane",
		pipelineName, pipelineType, ns)))
	return nil
}

func validPipelineType(t string) bool {
	switch t {
	case "build", "deploy", "test", "release", "integration":
		return true
	}
	return false
}
