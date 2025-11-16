/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the file at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package pipeline

import (
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
)

// PipelineCmd represents the pipeline command
var PipelineCmd = &cobra.Command{
	Use:   "pipeline",
	Short: "Manage CI/CD pipelines",
	Long: `Manage CI/CD pipelines and workflows for the Adhar platform.
	
This command provides:
• Pipeline creation and configuration
• Build and deployment automation
• Pipeline monitoring and status
• Artifact management and storage
• Pipeline templates and reuse
• Integration with GitOps workflows

Examples:
  adhar pipeline list                    # List all pipelines
  adhar pipeline create --name=deploy   # Create new pipeline
  adhar pipeline run --name=deploy      # Run pipeline
  adhar pipeline status --name=deploy   # Check pipeline status`,
	RunE: runPipeline,
}

var (
	// Pipeline command flags
	pipelineName string
	pipelineType string
	namespace    string
	service      string
	timeout      string
	output       string
	detailed     bool
)

func init() {
	// Pipeline command flags
	PipelineCmd.Flags().StringVarP(&pipelineName, "name", "n", "", "Pipeline name")
	PipelineCmd.Flags().StringVarP(&pipelineType, "type", "t", "", "Pipeline type (build, deploy, test)")
	PipelineCmd.Flags().StringVarP(&namespace, "namespace", "s", "", "Namespace")
	PipelineCmd.Flags().StringVarP(&service, "service", "e", "", "Service name")
	PipelineCmd.Flags().StringVarP(&timeout, "timeout", "i", "30m", "Operation timeout")
	PipelineCmd.Flags().StringVarP(&output, "output", "f", "", "Output format (table, json, yaml)")
	PipelineCmd.Flags().BoolVarP(&detailed, "detailed", "d", false, "Show detailed information")

	// Add subcommands
	PipelineCmd.AddCommand(listCmd)
	PipelineCmd.AddCommand(createCmd)
	PipelineCmd.AddCommand(runCmd)
	PipelineCmd.AddCommand(statusCmd)
	PipelineCmd.AddCommand(templateCmd)
}

func runPipeline(cmd *cobra.Command, args []string) error {
	logger.Info("🔧 Pipeline management - use subcommands for specific pipeline tasks")
	logger.Info("Available subcommands:")
	logger.Info("  list     - List all pipelines")
	logger.Info("  create   - Create new pipelines")
	logger.Info("  run      - Run pipelines")
	logger.Info("  status   - Check pipeline status")
	logger.Info("  template - Manage pipeline templates")

	return cmd.Help()
}
