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

package storage

import (
	"fmt"
	"time"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/k8s"

	"k8s.io/client-go/kubernetes"
)

// resolveNamespace returns the namespace flag value, defaulting to the Adhar
// system namespace when not set.
func resolveNamespace() string {
	if namespace != "" {
		return namespace
	}
	return globals.AdharSystemNamespace
}

// parseTimeout parses the --timeout flag, falling back to 5m on error.
func parseTimeout() time.Duration {
	if timeout == "" {
		return 5 * time.Minute
	}
	d, err := time.ParseDuration(timeout)
	if err != nil {
		return 5 * time.Minute
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
