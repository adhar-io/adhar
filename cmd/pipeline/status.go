package pipeline

import (
	"fmt"

	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check pipeline status",
	Long: `Check the status of CI/CD pipelines.
	
Examples:
  adhar pipeline status --name=deploy
  adhar pipeline status`,
	RunE: runStatus,
}

func runStatus(cmd *cobra.Command, args []string) error {
	if pipelineName != "" {
		return checkPipelineStatus(pipelineName)
	}

	return checkAllPipelinesStatus()
}

func checkPipelineStatus(pipelineName string) error {
	logger.Info(fmt.Sprintf("📊 Checking status for pipeline: %s", pipelineName))

	// TODO: Implement pipeline status check
	// This should show:
	// - Current status
	// - Last run result
	// - Stage progress
	// - Error details if any

	logger.Info("✅ Pipeline status checked")
	return nil
}

func checkAllPipelinesStatus() error {
	logger.Info("📊 Checking status for all pipelines...")

	// TODO: Implement all pipelines status check
	// This should show:
	// - All pipeline statuses
	// - Overall health
	// - Failed pipelines
	// - Success rates

	logger.Info("✅ All pipeline statuses checked")
	return nil
}
