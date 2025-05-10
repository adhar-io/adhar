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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// EnvironmentProvider represents the supported environment providers.
type EnvironmentProvider string

const (
	ProviderGKE   EnvironmentProvider = "gke"
	ProviderAWS   EnvironmentProvider = "aws"
	ProviderDO    EnvironmentProvider = "do"
	ProviderAzure EnvironmentProvider = "azure"
	ProviderCivo  EnvironmentProvider = "civo"
	ProviderKind  EnvironmentProvider = "kind"
)

// AddonSpec represents the specification for an addon.
type AddonSpec struct {
	Name  string    `json:"name"`
	Chart ChartSpec `json:"chart"`
}

// ChartSpec represents the Helm chart specification.
type ChartSpec struct {
	Repository string `json:"repository"`
	Name       string `json:"name"`
	Version    string `json:"version"`
}

// ValuesConfig represents a key-value pair for configuration values.
type ValuesConfig struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// HelmChartConfig represents the configuration for a Helm chart.
type HelmChartConfig struct {
	Chart  ChartSpec      `json:"chart"`
	Values []ValuesConfig `json:"values,omitempty"` // Updated type
}

// CoreServicesSpec defines the core services configuration for an environment.
type CoreServicesSpec struct {
	GitProvider string           `json:"gitProvider,omitempty"`
	Cilium      *HelmChartConfig `json:"cilium,omitempty"`
	Nginx       *HelmChartConfig `json:"nginx,omitempty"`
	Gitea       *HelmChartConfig `json:"gitea,omitempty"`
	ArgoCD      *HelmChartConfig `json:"argocd,omitempty"`
	Values      []ValuesConfig   `json:"values,omitempty"` // Updated type
}

// AdharPlatformSpec defines the desired state of AdharPlatform.
type AdharPlatformSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// Foo is an example field of AdharPlatform. Edit adharplatform_types.go to remove/update
	Foo string `json:"foo,omitempty"`
}

// AdharPlatformStatus defines the observed state of AdharPlatform.
type AdharPlatformStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// AdharPlatform is the Schema for the adharplatforms API.
type AdharPlatform struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AdharPlatformSpec   `json:"spec,omitempty"`
	Status AdharPlatformStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AdharPlatformList contains a list of AdharPlatform.
type AdharPlatformList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AdharPlatform `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AdharPlatform{}, &AdharPlatformList{})
}
