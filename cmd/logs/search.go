package logs

import (
	"fmt"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
)

var searchCmd = &cobra.Command{
	Use:   "search [query]",
	Short: "Search through platform logs",
	Long: `Search through platform logs using various criteria.
	
Examples:
  adhar logs search "error"
  adhar logs search "timeout" --component=nginx
  adhar logs search "deployment failed" --namespace=prod`,
	Args: cobra.ExactArgs(1),
	RunE: runSearch,
}

func runSearch(cmd *cobra.Command, args []string) error {
	query := args[0]
	logger.Info("🔍 Searching logs for: " + query)

	clientset, err := getClientset()
	if err != nil {
		return clusterError(err)
	}

	ctx, cancel := signalContext()
	defer cancel()

	total := 0

	if component != "" {
		// Search a single component/app.
		t := resolveTarget(component, namespace)
		fmt.Printf("\n%s\n", helpers.TitleStyle.Render("📦 "+component))
		n, err := streamPodLogs(ctx, clientset, t, int64(lines), false, query)
		if err != nil {
			return err
		}
		total += n
	} else if namespace != "" {
		// Search every pod in a namespace.
		t := componentTarget{Namespace: namespace, Selector: ""}
		n, err := streamPodLogs(ctx, clientset, t, int64(lines), false, query)
		if err != nil {
			return err
		}
		total += n
	} else {
		// Search across all core components.
		for name := range knownComponents {
			if name == "envoy" {
				continue
			}
			t := resolveTarget(name, namespace)
			n, err := streamPodLogs(ctx, clientset, t, int64(lines), false, query)
			if err != nil {
				fmt.Println(helpers.CreateMuted("  " + err.Error()))
				continue
			}
			total += n
		}
	}

	fmt.Println(helpers.CreateMuted(fmt.Sprintf("\n%d matching line(s) found.", total)))
	logger.Info("✅ Log search completed")
	return nil
}
