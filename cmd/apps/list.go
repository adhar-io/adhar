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

package apps

import (
	"fmt"

	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
)

// listCmd represents the list command
var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List all applications",
	Long: `List all applications in the specified namespace or across all namespaces.
	
Examples:
  adhar apps list
  adhar apps list --all-namespaces
  adhar apps list --namespace=production`,
	RunE: runList,
}

var (
	// List-specific flags
	allNamespaces bool
	showLabels    bool
	selector      string
)

func init() {
	listCmd.Flags().BoolVarP(&allNamespaces, "all-namespaces", "A", false, "List applications across all namespaces")
	listCmd.Flags().BoolVar(&showLabels, "show-labels", false, "Include application labels in the output")
	listCmd.Flags().StringVarP(&selector, "selector", "l", "", "Label selector to filter applications")
}

func runList(cmd *cobra.Command, args []string) error {
	logger.Info("📋 Listing applications...")

	kubeconfigPath, err := cmd.Root().PersistentFlags().GetString("kubeconfig")
	if err != nil {
		return fmt.Errorf("read kubeconfig flag: %w", err)
	}

	statuses, err := ListApplications(cmd.Context(), kubeconfigPath, namespace, allNamespaces, selector)
	if err != nil {
		return err
	}

	return RenderApplicationStatusList(statuses, output, showLabels)
}
