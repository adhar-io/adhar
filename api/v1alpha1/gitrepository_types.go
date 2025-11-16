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

const (
	GitProviderGitea     = "gitea"
	GitProviderGitlab    = "gitlab"
	GitProviderGithub    = "github"
	GitProviderBitbucket = "bitbucket"
	GiteaAdminUserName   = "giteaAdmin"
	SourceTypeLocal      = "local"
	SourceTypeRemote     = "remote"
	SourceTypeEmbedded   = "embedded"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// GitRepositorySpec defines the desired state of GitRepository.
type GitRepositorySpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file
	// +kubebuilder:validation:Optional
	Customization PackageCustomization `json:"customization,omitempty"`
	// SecretRef is the reference to secret that contain Git server credentials
	// +kubebuilder:validation:Optional
	SecretRef SecretReference     `json:"secretRef"`
	Source    GitRepositorySource `json:"source,omitempty"`
	Provider  Provider            `json:"provider"`
}

type GitRepositorySource struct {
	// +kubebuilder:validation:Enum:=argocd;gitea;nginx
	// +kubebuilder:validation:Optional
	EmbeddedAppName string `json:"embeddedAppName,omitempty"`
	// Path is the absolute path to directory that contains Kustomize structure or raw manifests.
	// This is required when Type is set to local.
	// +kubebuilder:validation:Optional
	Path             string               `json:"path"`
	RemoteRepository RemoteRepositorySpec `json:"remoteRepository"`
	// Type is the source type.
	// +kubebuilder:validation:Enum:=local;embedded;remote
	// +kubebuilder:default:=embedded
	Type string `json:"type"`
}

type Provider struct {
	// +kubebuilder:validation:Enum:=gitea;github
	// +kubebuilder:validation:Required
	Name string `json:"name"`
	// GitURL is the base URL of Git server used for API calls.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^https?:\/\/.+$`
	GitURL string `json:"gitURL"`
	// InternalGitURL is the base URL of Git server accessible within the cluster only.
	InternalGitURL   string `json:"internalGitURL"`
	OrganizationName string `json:"organizationName"`
}

type SecretReference struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

type Commit struct {
	// Hash is the digest of the most recent commit
	// +kubebuilder:validation:Optional
	Hash string `json:"hash"`
}

// GitRepositoryStatus defines the observed state of GitRepository.
type GitRepositoryStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
	// LatestCommit is the most recent commit known to the controller
	// +kubebuilder:validation:Optional
	LatestCommit Commit `json:"commit"`
	// ExternalGitRepositoryUrl is the url for the in-cluster repository accessible from local machine.
	// +kubebuilder:validation:Optional
	ExternalGitRepositoryUrl string `json:"externalGitRepositoryUrl"`
	// InternalGitRepositoryUrl is the url for the in-cluster repository accessible within the cluster.
	// +kubebuilder:validation:Optional
	InternalGitRepositoryUrl string `json:"internalGitRepositoryUrl"`
	// Path is the path within the repository that contains the files.
	// +kubebuilder:validation:Optional
	Path   string `json:"path"`
	Synced bool   `json:"synced"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// GitRepository is the Schema for the gitrepositories API.
type GitRepository struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   GitRepositorySpec   `json:"spec,omitempty"`
	Status GitRepositoryStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// GitRepositoryList contains a list of GitRepository.
type GitRepositoryList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []GitRepository `json:"items"`
}

func init() {
	SchemeBuilder.Register(&GitRepository{}, &GitRepositoryList{})
}
