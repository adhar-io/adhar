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
	"context"
	"fmt"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// scaleCmd represents the scale command
var scaleCmd = &cobra.Command{
	Use:   "scale [app-name] --replicas=N",
	Short: "Scale an application",
	Long: `Scale an application's Deployment to the specified number of replicas.

Examples:
  adhar apps scale my-app --replicas=3
  adhar apps scale my-app --replicas=0 --namespace=platform-apps`,
	Args: cobra.ExactArgs(1),
	RunE: runScale,
}

var (
	// Scale-specific flags
	replicas int32
)

func init() {
	scaleCmd.Flags().Int32VarP(&replicas, "replicas", "r", 1, "Number of replicas")
	if err := scaleCmd.MarkFlagRequired("replicas"); err != nil {
		panic(err)
	}
}

func runScale(cmd *cobra.Command, args []string) error {
	appName := args[0]

	if replicas < 0 {
		return fmt.Errorf("--replicas must be >= 0, got %d", replicas)
	}

	scaleNamespace := namespace
	if scaleNamespace == "" {
		scaleNamespace = "default"
	}

	logger.Info(fmt.Sprintf("📈 Scaling application %s to %d replicas", appName, replicas))

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

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("create kubernetes client: %w", err)
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	deployments := clientset.AppsV1().Deployments(scaleNamespace)

	// Confirm the deployment exists before issuing the scale subresource update so
	// we can surface a friendly not-found message.
	if _, err := deployments.Get(ctx, appName, metav1.GetOptions{}); err != nil {
		if k8serrors.IsNotFound(err) {
			return fmt.Errorf("deployment %q not found in namespace %q", appName, scaleNamespace)
		}
		return fmt.Errorf("get deployment: %w", err)
	}

	scale := &autoscalingv1.Scale{
		ObjectMeta: metav1.ObjectMeta{
			Name:      appName,
			Namespace: scaleNamespace,
		},
		Spec: autoscalingv1.ScaleSpec{
			Replicas: replicas,
		},
	}

	if _, err := deployments.UpdateScale(ctx, appName, scale, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("scale deployment: %w", err)
	}

	fmt.Println(helpers.CreateSuccess(fmt.Sprintf("Scaled %s to %d replicas in namespace %s", appName, replicas, scaleNamespace)))
	return nil
}
