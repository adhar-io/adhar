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
	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
)

// deleteCmd represents the delete command
var deleteCmd = &cobra.Command{
	Use:   "delete [app-name]",
	Short: "Delete an application",
	Long: `Delete an application and all its resources.

The application is an Adhar platform Application claim (platform.adhar.io/v1alpha1).
Deleting it removes the claim and lets Crossplane/ArgoCD garbage-collect the
managed workloads.

Without --namespace the application is looked for in adhar-system, where
template deploys register it, then in the dev environment.

Examples:
  adhar application delete my-app
  adhar application delete my-app --namespace=platform-apps
  adhar application delete my-app --force`,
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

	kubeconfigPath, err := cmd.Root().PersistentFlags().GetString("kubeconfig")
	if err != nil {
		return fmt.Errorf("read kubeconfig flag: %w", err)
	}
	dynamicClient, err := dynamicClientFor(kubeconfigPath)
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	return deleteApplication(ctx, dynamicClient, applicationNamespaces(cmd.Flags().Changed("namespace"), namespace), appName,
		func(deleteNamespace string) bool {
			if force {
				return true
			}
			helpers.Printf("Delete application %q in namespace %q? [y/N]: ", appName, deleteNamespace)
			reader := bufio.NewReader(os.Stdin)
			answer, _ := reader.ReadString('\n')
			answer = strings.ToLower(strings.TrimSpace(answer))
			return answer == "y" || answer == "yes"
		})
}

// deleteApplication deletes the Application named appName from the first of
// namespaces that holds one, once confirm agrees to that namespace.
func deleteApplication(ctx context.Context, dynamicClient dynamic.Interface, namespaces []string, appName string, confirm func(namespace string) bool) error {
	// Find it first, so the question names the namespace it is actually in:
	// a template deploy registers its Application in adhar-system, not dev.
	app, err := findApplication(ctx, dynamicClient, namespaces, appName)
	if err != nil {
		return err
	}
	deleteNamespace := app.GetNamespace()

	// An Application Argo CD keeps from Git comes straight back: the SPA
	// templates commit it as .adhar/app.yaml, which the adhar-services
	// ApplicationSet applies with self-heal. Deleting it here took down every
	// environment's deployment, reported success, and was undone a sync later.
	if keeper := argoCDKeeper(ctx, dynamicClient, app); keeper != nil {
		return keeperRefusal(app, keeper)
	}

	if !confirm(deleteNamespace) {
		logger.Info("Aborted.")
		return nil
	}

	logger.Info(fmt.Sprintf("✖ Deleting application: %s", appName))

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

// argoCDTrackingAnnotation is where Argo CD (annotation tracking, its default
// since v3) records which of its Applications manages an object:
// `<app>:<group>/<kind>:<namespace>/<name>`, the app written
// `<namespace>_<name>` when it lives outside Argo CD's own namespace.
const argoCDTrackingAnnotation = "argocd.argoproj.io/tracking-id"

// argoCDKeeper returns the Argo CD Application that keeps app in the cluster
// from Git, or nil when none does or that cannot be told (a reader who may not
// see Argo CD's Applications is not stopped by this check). A tracking id
// counts only for the object it was written for — Argo CD ignores one copied
// onto another — and only while the Application it names still exists.
func argoCDKeeper(ctx context.Context, dc dynamic.Interface, app *unstructured.Unstructured) *unstructured.Unstructured {
	keeper, target, ok := strings.Cut(app.GetAnnotations()[argoCDTrackingAnnotation], ":")
	if !ok || keeper == "" {
		return nil
	}
	gvk := app.GroupVersionKind()
	if target != gvk.Group+"/"+gvk.Kind+":"+app.GetNamespace()+"/"+app.GetName() {
		return nil
	}
	ns, name := globals.AdharSystemNamespace, keeper
	if n, a, found := strings.Cut(keeper, "_"); found {
		ns, name = n, a
	}
	obj, err := dc.Resource(argoApplicationGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil
	}
	return obj
}

// keeperRefusal says why app is not deleted, and what retires it instead.
func keeperRefusal(app, keeper *unstructured.Unstructured) error {
	who := keeper.GetName()
	if keeper.GetNamespace() != globals.AdharSystemNamespace {
		who = keeper.GetNamespace() + "/" + who
	}
	how := "remove it from the Git source " + who + " syncs, then delete here whatever is left"
	if labels := keeper.GetLabels(); labels["adhar.io/type"] == "service-bootstrap" && labels["adhar.io/service"] != "" {
		how = fmt.Sprintf("delete %s from the repository %s/%s (the adhar-services ApplicationSet then drops %s), then delete here whatever is left",
			controlPlaneDescriptor, globals.GiteaPlatformOrg, labels["adhar.io/service"], who)
	}
	return fmt.Errorf("application %s/%s is kept from Git by Argo CD application %s: deleting it here would take down every environment's "+
		"deployment until that application's next sync put it back; to retire it, %s",
		app.GetNamespace(), app.GetName(), who, how)
}
