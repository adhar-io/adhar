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
	"adhar-io/adhar/globals"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// LastObservedCLIStartTimeAnnotation indicates when the controller acted on a resource.
	LastObservedCLIStartTimeAnnotation = "adhar.io/last-observed-cli-start-time"
	// CliStartTimeAnnotation indicates when the CLI was invoked.
	CliStartTimeAnnotation = "adhar.io/cli-start-time"
	FieldManager           = "adhar"
	// If GetSecretLabelKey is set to GetSecretLabelValue on a kubernetes secret, secret key and values can be used by the get command.
	CLISecretLabelKey      = "adhar.io/cli-secret"
	CLISecretLabelValue    = "true"
	PackageNameLabelKey    = "adhar.io/package-name"
	PackageTypeLabelKey    = "adhar.io/package-type"
	PackageTypeLabelCore   = "core"
	PackageTypeLabelCustom = "custom"

	ArgoCDPackageName       = "argocd"
	GiteaPackageName        = "gitea"
	IngressNginxPackageName = "nginx"
	CiliumPackageName       = "cilium"
	CrossplanePackageName   = "crosplane"
)

const (
	ProviderDO    EnvironmentProvider = "do"
	ProviderGKE   EnvironmentProvider = "gke"
	ProviderAWS   EnvironmentProvider = "aws"
	ProviderAzure EnvironmentProvider = "azure"
	ProviderCivo  EnvironmentProvider = "civo"
	ProviderKind  EnvironmentProvider = "kind"
)

// AdharPlatformSpec defines the desired state of AdharPlatform.
type AdharPlatformSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file
	PackageConfigs     PackageConfigsSpec     `json:"packageConfigs,omitempty"`
	BuildCustomization BuildCustomizationSpec `json:"buildCustomization,omitempty"`
}

// ArgoPackageConfigSpec Allows for configuration of the ArgoCD Installation.
// If no fields are specified then the binary embedded resources will be used to install ArgoCD.
type ArgoPackageConfigSpec struct {
	// Enabled controls whether to install ArgoCD.
	Enabled bool `json:"enabled,omitempty"`
}

// EmbeddedArgoApplicationsPackageConfigSpec Controls the installation of the embedded argo applications.
type EmbeddedArgoApplicationsPackageConfigSpec struct {
	// Enabled controls whether to install the embedded argo applications and the associated GitServer
	Enabled bool `json:"enabled,omitempty"`
}

type PackageConfigsSpec struct {
	Argo                     ArgoPackageConfigSpec                     `json:"argoPackageConfigs,omitempty"`
	EmbeddedArgoApplications EmbeddedArgoApplicationsPackageConfigSpec `json:"embeddedArgoApplicationsPackageConfigs,omitempty"`
	CustomPackageDirs        []string                                  `json:"customPackageDirs,omitempty"`
	CustomPackageUrls        []string                                  `json:"customPackageUrls,omitempty"`
	// +kubebuilder:validation:Optional
	CorePackageCustomization map[string]PackageCustomization `json:"packageCustomization,omitempty"`
}

// BuildCustomizationSpec fields cannot change once a cluster is created
type BuildCustomizationSpec struct {
	Protocol       string `json:"protocol,omitempty"`
	Host           string `json:"host,omitempty"`
	IngressHost    string `json:"ingressHost,omitempty"`
	Port           string `json:"port,omitempty"`
	UsePathRouting bool   `json:"usePathRouting,omitempty"`
	SelfSignedCert string `json:"selfSignedCert,omitempty"`
	StaticPassword bool   `json:"staticPassword,omitempty"`
}

// PackageCustomization defines how packages are customized
type PackageCustomization struct {
	// Name is the name of the package to be customized. e.g. argocd
	Name string `json:"name,omitempty"`
	// FilePath is the absolute file path to a YAML file that contains Kubernetes manifests.
	FilePath string `json:"filePath,omitempty"`
}

// CoreServicesSpec defines the configuration for core services.
type CoreServicesSpec struct {
	Cilium *HelmChartConfig `json:"cilium,omitempty"`
	Nginx  *HelmChartConfig `json:"nginx,omitempty"`
	Gitea  *HelmChartConfig `json:"gitea,omitempty"`
	ArgoCD *HelmChartConfig `json:"argocd,omitempty"`
	Values []ValuesConfig   `json:"values,omitempty"`
}

// AddonSpec defines the configuration for an addon.
type AddonSpec struct {
	Name   string         `json:"name"`
	Chart  ChartSpec      `json:"chart"`
	Values []ValuesConfig `json:"values,omitempty"`
}

// EnvironmentProvider defines the provider for an environment.
type EnvironmentProvider string

// HelmChartConfig defines the configuration for a Helm chart.
type HelmChartConfig struct {
	Chart  ChartSpec      `json:"chart"`
	Values []ValuesConfig `json:"values,omitempty"`
}

// ChartSpec defines the specification for a Helm chart.
type ChartSpec struct {
	Repository string `json:"repository"`
	Name       string `json:"name"`
	Version    string `json:"version"`
}

// ValuesConfig defines a key-value pair for Helm chart values.
type ValuesConfig struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// AdharPlatformStatus defines the observed state of AdharPlatform.
type AdharPlatformStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
	// ObservedGeneration is the 'Generation' of the Service that was last processed by the controller.
	// +optional
	ObservedGeneration int64        `json:"observedGeneration,omitempty"`
	ArgoCD             ArgoCDStatus `json:"ArgoCD,omitempty"`
	Nginx              NginxStatus  `json:"nginx,omitempty"`
	Gitea              GiteaStatus  `json:"gitea,omitempty"`
}

type GiteaStatus struct {
	Available                bool   `json:"available,omitempty"`
	ExternalURL              string `json:"externalURL,omitempty"`
	InternalURL              string `json:"internalURL,omitempty"`
	AdminUserSecretName      string `json:"adminUserSecretNameecret,omitempty"`
	AdminUserSecretNamespace string `json:"adminUserSecretNamespace,omitempty"`
	RepositoriesCreated      bool   `json:"repositoriesCreated,omitempty"`
}

type ArgoCDStatus struct {
	Available   bool `json:"available,omitempty"`
	AppsCreated bool `json:"appsCreated,omitempty"`
}

type NginxStatus struct {
	Available bool `json:"available,omitempty"`
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

func (l *AdharPlatform) GetArgoProjectName() string {
	return fmt.Sprintf("%s-%s-gitserver", globals.ProjectName, l.Name)
}

func (l *AdharPlatform) GetArgoApplicationName(name string) string {
	return fmt.Sprintf("%s-%s-gitserver-%s", globals.ProjectName, l.Name, name)
}
