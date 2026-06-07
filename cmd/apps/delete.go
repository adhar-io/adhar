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
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
)

// deleteCmd represents the delete command
var deleteCmd = &cobra.Command{
	Use:   "delete [app-name]",
	Short: "Delete an application",
	Long: `Delete an application and all its resources.

The application is an Adhar platform Application claim (platform.adhar.io/v1alpha1).
Deleting it removes the claim and lets Crossplane/ArgoCD garbage-collect the
managed workloads.

Examples:
  adhar apps delete my-app
  adhar apps delete my-app --namespace=platform-apps
  adhar apps delete my-app --force`,
	Args: cobra.ExactArgs(1),
	RunE: runDelete,
}

var (
	// Delete-specific flags
	force bool
)

func init() {
	deleteCmd.Flags().BoolVarP(&force, "force", "f", false, "Force deletion without confirmation")
}

func runDelete(cmd *cobra.Command, args []string) error {
	appName := args[0]

	deleteNamespace := namespace
	if deleteNamespace == "" {
		deleteNamespace = "default"
	}

	if !force {
		fmt.Printf("Delete application %q in namespace %q? [y/N]: ", appName, deleteNamespace)
		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		answer = strings.ToLower(strings.TrimSpace(answer))
		if answer != "y" && answer != "yes" {
			logger.Info("Aborted.")
			return nil
		}
	}

	logger.Info(fmt.Sprintf("🗑️  Deleting application: %s", appName))

	kubeconfigPath, err := cmd.Root().PersistentFlags().GetString("kubeconfig")
	if err != nil {
		return fmt.Errorf("read kubeconfig flag: %w", err)
	}
	if kubeconfigPath == "" {
		kubeconfigPath = helpers.GetKubeConfigPath()
	}

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		return fmt.Errorf("build kubeconfig: %w", err)
	}

	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("create dynamic client: %w", err)
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	err = dynamicClient.Resource(applicationGVR).Namespace(deleteNamespace).Delete(ctx, appName, metav1.DeleteOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return fmt.Errorf("%w: %s/%s", ErrApplicationNotFound, deleteNamespace, appName)
		}
		return fmt.Errorf("delete application: %w", err)
	}

	fmt.Println(helpers.CreateSuccess(fmt.Sprintf("Application %s deleted from namespace %s", appName, deleteNamespace)))
	return nil
}
