/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package network

import (
	"context"
	"fmt"
	"time"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/k8s"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// resolveNamespace returns the namespace flag value, defaulting to the Adhar
// system namespace when not set. An empty value with allowAll lists all
// namespaces.
func resolveNamespace() string {
	if namespace != "" {
		return namespace
	}
	return globals.AdharSystemNamespace
}

// parseTimeout parses the --timeout flag, falling back to 30s on error.
func parseTimeout() time.Duration {
	if timeout == "" {
		return 30 * time.Second
	}
	d, err := time.ParseDuration(timeout)
	if err != nil {
		return 30 * time.Second
	}
	return d
}

// getClientset builds a clientset using the shared platform helper and prints a
// friendly message when the cluster cannot be reached.
func getClientset() (*kubernetes.Clientset, error) {
	clientset, err := k8s.GetClientset()
	if err != nil {
		fmt.Println(helpers.ErrorStyle.Render("❌ Could not connect to the cluster"))
		fmt.Println(helpers.CreateMuted("   " + err.Error()))
		fmt.Println(helpers.CreateMuted("   Is the cluster running? Try `adhar up` or check your kubeconfig context."))
		return nil, fmt.Errorf("failed to get Kubernetes client: %w", err)
	}
	return clientset, nil
}

// ciliumStatus returns a short, best-effort description of the Cilium CNI
// agent DaemonSet in the Adhar system namespace. It never returns an error;
// callers display the string as-is.
func ciliumStatus(ctx context.Context, clientset *kubernetes.Clientset) string {
	dsList, err := clientset.AppsV1().DaemonSets(globals.AdharSystemNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/name=cilium-agent",
	})
	if err != nil || len(dsList.Items) == 0 {
		return "Cilium agent not detected"
	}
	ds := dsList.Items[0]
	return fmt.Sprintf("cilium-agent %d/%d ready", ds.Status.NumberReady, ds.Status.DesiredNumberScheduled)
}
