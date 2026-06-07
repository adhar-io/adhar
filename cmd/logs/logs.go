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

package logs

import (
	"fmt"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
)

// LogsCmd represents the logs command
var LogsCmd = &cobra.Command{
	Use:   "logs",
	Short: "View platform logs and events",
	Long: `View centralized logs and events from the Adhar platform.
	
This command provides:
• Centralized log viewing across all components
• Real-time log streaming
• Log filtering and search
• Component-specific logs
• Log aggregation and analysis

Examples:
  adhar logs                      # View all platform logs
  adhar logs --follow             # Follow logs in real-time
  adhar logs --component=nginx    # Component-specific logs
  adhar logs --namespace=prod     # Namespace-specific logs
  adhar logs --search=error       # Search for error logs`,
	RunE: runLogs,
}

var (
	// Logs command flags
	follow    bool
	component string
	namespace string
	search    string
	lines     int
	since     string
	until     string
	level     string
	output    string
)

func init() {
	// Logs command flags
	LogsCmd.Flags().BoolVarP(&follow, "follow", "f", false, "Follow logs in real-time")
	LogsCmd.Flags().StringVarP(&component, "component", "c", "", "Show logs for specific component")
	LogsCmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Show logs for specific namespace")
	LogsCmd.Flags().StringVarP(&search, "search", "s", "", "Search for specific text in logs")
	LogsCmd.Flags().IntVarP(&lines, "lines", "l", 100, "Number of lines to show")
	LogsCmd.Flags().StringVarP(&since, "since", "", "", "Show logs since timestamp")
	LogsCmd.Flags().StringVarP(&until, "until", "", "", "Show logs until timestamp")
	LogsCmd.Flags().StringVarP(&level, "level", "", "", "Filter logs by level (debug, info, warn, error)")
	LogsCmd.Flags().StringVarP(&output, "output", "o", "", "Output logs to file")

	// Add subcommands
	LogsCmd.AddCommand(streamCmd)
	LogsCmd.AddCommand(searchCmd)
	LogsCmd.AddCommand(exportCmd)
}

func runLogs(cmd *cobra.Command, args []string) error {
	logger.Info("📋 Viewing platform logs...")

	if component != "" {
		return viewComponentLogs(component)
	}

	if namespace != "" {
		return viewNamespaceLogs(namespace)
	}

	return viewAllLogs()
}

// viewAllLogs streams logs for every core platform component.
func viewAllLogs() error {
	logger.Info("🔍 Viewing all platform logs...")

	clientset, err := getClientset()
	if err != nil {
		return clusterError(err)
	}

	ctx, cancel := signalContext()
	defer cancel()

	// Following all components at once is ambiguous, so --follow only applies to
	// a single component; for "all" we print the recent tail of each.
	for name := range knownComponents {
		// Skip the alias so we do not print Cilium Envoy twice.
		if name == "envoy" {
			continue
		}
		t := resolveTarget(name, namespace)
		fmt.Printf("\n%s\n", helpers.TitleStyle.Render("📦 "+name))
		if _, err := streamPodLogs(ctx, clientset, t, int64(lines), false, search); err != nil {
			fmt.Println(helpers.CreateMuted("  " + err.Error()))
		}
	}

	logger.Info("✅ All logs displayed")
	return nil
}

// viewComponentLogs streams logs for a single named component or app.
func viewComponentLogs(componentName string) error {
	logger.Info("🔍 Viewing component logs: " + componentName)

	clientset, err := getClientset()
	if err != nil {
		return clusterError(err)
	}

	ctx, cancel := signalContext()
	defer cancel()

	t := resolveTarget(componentName, namespace)
	if follow {
		fmt.Println(helpers.CreateMuted("Following logs (press Ctrl-C to stop)..."))
	}
	_, err = streamPodLogs(ctx, clientset, t, int64(lines), follow, search)
	if err != nil {
		return err
	}

	logger.Info("✅ Component logs displayed")
	return nil
}

// viewNamespaceLogs streams logs for every pod in a namespace.
func viewNamespaceLogs(namespaceName string) error {
	logger.Info("🔍 Viewing namespace logs: " + namespaceName)

	clientset, err := getClientset()
	if err != nil {
		return clusterError(err)
	}

	ctx, cancel := signalContext()
	defer cancel()

	// Empty selector matches all pods in the namespace.
	t := componentTarget{Namespace: namespaceName, Selector: ""}
	if _, err := streamPodLogs(ctx, clientset, t, int64(lines), follow, search); err != nil {
		return err
	}

	logger.Info("✅ Namespace logs displayed")
	return nil
}

// clusterError prints a friendly message and returns a wrapped error when the
// cluster cannot be reached.
func clusterError(err error) error {
	fmt.Println(helpers.ErrorStyle.Render("❌ Could not connect to the cluster"))
	fmt.Println(helpers.CreateMuted("   " + err.Error()))
	fmt.Println(helpers.CreateMuted("   Is the cluster running? Try `adhar up` or check your kubeconfig context."))
	return fmt.Errorf("failed to get Kubernetes client: %w", err)
}
