package logs

import (
	"fmt"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
)

var streamCmd = &cobra.Command{
	Use:   "stream",
	Short: "Stream logs in real-time",
	Long: `Stream platform logs in real-time with filtering options.
	
Examples:
  adhar logs stream
  adhar logs stream --component=argocd
  adhar logs stream --level=error`,
	RunE: runStream,
}

func runStream(cmd *cobra.Command, args []string) error {
	logger.Info("📡 Streaming logs in real-time...")

	clientset, err := getClientset()
	if err != nil {
		return clusterError(err)
	}

	ctx, cancel := signalContext()
	defer cancel()

	// Default to all core components when no specific component is requested.
	name := component
	if name == "" {
		name = "argocd"
		fmt.Println(helpers.CreateMuted("No --component specified; streaming argocd. Use --component to choose another."))
	}

	t := resolveTarget(name, namespace)
	fmt.Println(helpers.CreateMuted("Streaming logs (press Ctrl-C to stop)..."))
	if _, err := streamPodLogs(ctx, clientset, t, int64(lines), true, search); err != nil {
		return err
	}

	logger.Info("✅ Log streaming stopped")
	return nil
}
