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

package pipeline

import (
	"fmt"

	"adhar-io/adhar/cmd/helpers"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
)

// Argo Workflows resources. Pipelines on the Adhar platform are implemented as
// Argo Workflows / WorkflowTemplates (argoproj.io/v1alpha1).
var (
	workflowsGVR = schema.GroupVersionResource{
		Group:    "argoproj.io",
		Version:  "v1alpha1",
		Resource: "workflows",
	}
	workflowTemplatesGVR = schema.GroupVersionResource{
		Group:    "argoproj.io",
		Version:  "v1alpha1",
		Resource: "workflowtemplates",
	}
)

// defaultNamespace returns the namespace flag, defaulting to "argo" which is the
// conventional namespace for Argo Workflows on the platform.
func defaultNamespace() string {
	if namespace != "" {
		return namespace
	}
	return "argo"
}

// getDynamicClient builds a dynamic client from the standard kubeconfig and
// returns a friendly error when the cluster is unreachable.
func getDynamicClient() (dynamic.Interface, error) {
	kubeconfigPath := helpers.GetKubeConfigPath()
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("could not connect to the cluster (is it running? try `adhar up`): %w", err)
	}
	client, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("create dynamic client: %w", err)
	}
	return client, nil
}

// stringField returns a nested string value from an unstructured object map.
func stringField(obj map[string]interface{}, keys ...string) string {
	cur := obj
	for i, k := range keys {
		if i == len(keys)-1 {
			if v, ok := cur[k].(string); ok {
				return v
			}
			return ""
		}
		next, ok := cur[k].(map[string]interface{})
		if !ok {
			return ""
		}
		cur = next
	}
	return ""
}
