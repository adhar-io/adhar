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

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// DataPlaneInfraMode selects how the underlying cluster is realised.
type DataPlaneInfraMode string

const (
	InfraModeComposite DataPlaneInfraMode = "composite" // Crossplane CompositeCluster
	InfraModeVCluster  DataPlaneInfraMode = "vcluster"  // vcluster on the control plane (T1/T2)
	InfraModeAdopt     DataPlaneInfraMode = "adopt"     // register an existing kubeconfig
)

// DataPlaneProfile is the thin-agent bundle a data plane runs.
type DataPlaneProfile string

const (
	ProfileStandard DataPlaneProfile = "standard" // metrics-server, kyverno(+policies), alloy, eso-agent, spire-agent, gateway
	ProfileEdge     DataPlaneProfile = "edge"     // standard minus heavy collectors
	ProfileGPU      DataPlaneProfile = "gpu"      // standard + device-plugin, GPU feature discovery
	ProfileIsolated DataPlaneProfile = "isolated" // standard + stricter NetworkPolicies, dedicated node pools
)

// DataPlane condition types (metav1.Condition.Type values).
const (
	DataPlaneInfraReady         = "InfraReady"
	DataPlaneRegistered         = "Registered"
	DataPlaneAgentsReady        = "AgentsReady"
	DataPlaneMeshJoined         = "MeshJoined"
	DataPlaneObservabilityWired = "ObservabilityWired"
	DataPlaneReady              = "Ready" // aggregate
)

// Condition reasons.
const (
	ReasonProvisioning      = "Provisioning"
	ReasonInfraReady        = "InfraReady"
	ReasonRegistering       = "Registering"
	ReasonAgentsProgressing = "AgentsProgressing"
	ReasonMeshConnecting    = "MeshConnecting"
	ReasonReady             = "Ready"
	ReasonError             = "ReconcileError"
)

type NodePoolSpec struct {
	Name  string `json:"name"`
	Size  string `json:"size"`
	Count int    `json:"count"`
	// +optional
	GPU bool `json:"gpu,omitempty"`
}

type DataPlaneInfrastructure struct {
	// +kubebuilder:validation:Enum=composite;vcluster;adopt
	Mode DataPlaneInfraMode `json:"mode"`
	// +optional
	Provider EnvironmentProvider `json:"provider,omitempty"` // for mode=composite
	// +optional
	Region string `json:"region,omitempty"`
	// +optional
	NodePools []NodePoolSpec `json:"nodePools,omitempty"`
	// CompositeRef links the CompositeCluster XR the controller created (mode=composite).
	// +optional
	CompositeRef *NamedRef `json:"compositeRef,omitempty"`
	// KubeconfigSecretRef references a kubeconfig secret (mode=adopt).
	// +optional
	KubeconfigSecretRef *NamedRef `json:"kubeconfigSecretRef,omitempty"`
}

type NamedRef struct {
	Name string `json:"name"`
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

type DataPlaneMesh struct {
	// +optional
	Enabled bool `json:"enabled,omitempty"`
}

type DataPlaneObservability struct {
	// Hub is the control-plane mesh identity that stores telemetry (default "adhar-mgmt").
	// +optional
	Hub string `json:"hub,omitempty"`
}

type DataPlanePlacement struct {
	// Labels are stamped on the ArgoCD cluster secret so ApplicationSet
	// generators and Sveltos ClusterProfiles can select this plane.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
}

// DataPlaneSpec is the desired state.
type DataPlaneSpec struct {
	Infrastructure DataPlaneInfrastructure `json:"infrastructure"`
	// +kubebuilder:validation:Enum=standard;edge;gpu;isolated
	// +kubebuilder:default=standard
	Profile DataPlaneProfile `json:"profile,omitempty"`
	// +optional
	Mesh DataPlaneMesh `json:"mesh,omitempty"`
	// +optional
	Observability DataPlaneObservability `json:"observability,omitempty"`
	// +optional
	Placement DataPlanePlacement `json:"placement,omitempty"`
}

// DataPlaneStatus is the observed state.
type DataPlaneStatus struct {
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// +optional
	KubernetesVersion string `json:"kubernetesVersion,omitempty"`
	// +optional
	ArgoCDCluster string `json:"argocdCluster,omitempty"`
	// +optional
	Endpoint string `json:"endpoint,omitempty"`
	// +optional
	AppCount int `json:"appCount,omitempty"`
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=dp
// +kubebuilder:printcolumn:name="Mode",type=string,JSONPath=`.spec.infrastructure.mode`
// +kubebuilder:printcolumn:name="Provider",type=string,JSONPath=`.spec.infrastructure.provider`
// +kubebuilder:printcolumn:name="Apps",type=integer,JSONPath=`.status.appCount`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type DataPlane struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              DataPlaneSpec   `json:"spec,omitempty"`
	Status            DataPlaneStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type DataPlaneList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DataPlane `json:"items"`
}

func init() { SchemeBuilder.Register(&DataPlane{}, &DataPlaneList{}) }
