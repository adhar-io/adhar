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

package helpers

import (
	"context"
	"fmt"
	"os"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
)

// XRGroupVersion is the API group/version every Adhar composite resource (XR)
// belongs to. The control plane (Crossplane) reconciles these XRs into the real
// backing resources — locally via in-cluster compositions, on a cloud via the
// matching cloud composition. Both the CLI and the Adhar Console produce these
// same XRs, so a resource request is identical regardless of the entry point.
const (
	XRGroup   = "platform.adhar.io"
	XRVersion = "v1alpha1"

	// providerEnv overrides the composition provider used for selection.
	// Defaults to "local"; set to aws/azure/gcp/… on a cloud platform so the
	// same XR resolves to that cloud's composition.
	providerEnv = "ADHAR_PROVIDER"
)

// ActiveProvider reports which composition family the control plane should use
// to satisfy resource requests. Local Kind clusters use in-cluster compositions
// (provider "local"); cloud platforms set ADHAR_PROVIDER to their cloud so the
// identical XR resolves to e.g. AWS RDS instead of CloudNativePG.
func ActiveProvider() string {
	if p := os.Getenv(providerEnv); p != "" {
		return p
	}
	return "local"
}

// CompositionSelector builds the matchLabels a composite resource uses to pick
// its composition, keyed on the active provider plus the feature and any
// discriminators (e.g. engine=postgresql). Because every composition is labelled
// provider+feature+discriminator, this selection is unambiguous and portable:
// switching ActiveProvider swaps the whole backing implementation with no change
// to the caller. This is the single selection contract shared by CLI and Console.
func CompositionSelector(feature string, discriminators map[string]string) map[string]interface{} {
	labels := map[string]interface{}{
		"provider": ActiveProvider(),
		"feature":  feature,
	}
	for k, v := range discriminators {
		if v != "" {
			labels[k] = v
		}
	}
	return map[string]interface{}{"matchLabels": labels}
}

// XRGVR returns the GroupVersionResource for a composite resource plural, e.g.
// XRGVR("compositedatabases").
func XRGVR(plural string) schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: XRGroup, Version: XRVersion, Resource: plural}
}

// NewXR constructs a namespaced composite resource with a provider-aware
// compositionSelector already set. Callers supply the spec body (parameters,
// etc.); the selector is merged in so CLI-created XRs select compositions
// exactly the way Console-created ones do.
func NewXR(kind, name, namespace, feature string, discriminators map[string]string, spec map[string]interface{}) *unstructured.Unstructured {
	if spec == nil {
		spec = map[string]interface{}{}
	}
	// Crossplane v2 namespaced XRs configure composition selection under
	// spec.crossplane.* (not the top-level spec.compositionSelector of v1).
	spec["crossplane"] = map[string]interface{}{
		"compositionSelector": CompositionSelector(feature, discriminators),
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": XRGroup + "/" + XRVersion,
		"kind":       kind,
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": namespace,
			"labels": map[string]interface{}{
				"adhar.io/managed-by": "adhar-cli",
			},
		},
		"spec": spec,
	}}
}

// DynamicClient builds a dynamic client from the standard kubeconfig, used to
// apply composite resources to the control plane.
func DynamicClient() (dynamic.Interface, error) {
	config, err := clientcmd.BuildConfigFromFlags("", GetKubeConfigPath())
	if err != nil {
		return nil, fmt.Errorf("could not connect to the cluster (is it running? try `adhar up`): %w", err)
	}
	client, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("create dynamic client: %w", err)
	}
	return client, nil
}

// ApplyXR creates the composite resource in its namespace. The control plane
// then reconciles it — the same declarative, controller-driven path whether the
// XR came from the CLI or the Console. Returns nil if the resource already exists.
func ApplyXR(ctx context.Context, plural string, xr *unstructured.Unstructured) error {
	client, err := DynamicClient()
	if err != nil {
		return err
	}
	ns := xr.GetNamespace()
	_, err = client.Resource(XRGVR(plural)).Namespace(ns).Create(ctx, xr, metav1.CreateOptions{})
	return err
}
