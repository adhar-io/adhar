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

package db

import (
	"fmt"

	"adhar-io/adhar/cmd/helpers"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
)

// compositeDatabaseGVR identifies Crossplane CompositeDatabase XRs which back
// the platform's managed databases (platform.adhar.io/v1alpha1, namespaced).
var compositeDatabaseGVR = schema.GroupVersionResource{
	Group:    "platform.adhar.io",
	Version:  "v1alpha1",
	Resource: "compositedatabases",
}

// dbNamespace returns the namespace for database resources, defaulting to
// "default" when no namespace is provided.
func dbNamespace() string {
	if dbNS != "" {
		return dbNS
	}
	return "default"
}

// getDynamicClient builds a dynamic client from the standard kubeconfig.
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

func valueOrDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
