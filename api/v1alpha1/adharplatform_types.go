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

	ArgoCDPackageName         = "argocd"
	GiteaPackageName          = "gitea"
	GatewayPackageName        = "gateway"
	GatewayAPICRDsPackageName = "gateway-api-crds"
	CiliumPackageName         = "cilium"
	CNPGPackageName           = "cnpg"
	CrossplanePackageName     = "crossplane"
)

const (
	ProviderDO    EnvironmentProvider = "do"
	ProviderGKE   EnvironmentProvider = "gke"
	ProviderAWS   EnvironmentProvider = "aws"
	ProviderAzure EnvironmentProvider = "azure"
	ProviderCivo  EnvironmentProvider = "civo"
	ProviderKind  EnvironmentProvider = "kind"
	// ProviderCustom is a bring-your-own (on-prem) Kubernetes cluster.
	ProviderCustom EnvironmentProvider = "custom"
)

// AdharPlatformSpec defines the desired state of AdharPlatform.
type AdharPlatformSpec struct {
	// Provider is the infrastructure provider this platform runs on (kind,
	// aws, azure, gke, do, civo). The controller uses it to decide which
	// optional components make sense — e.g. cloud Crossplane providers are
	// only installed on cloud platforms. Empty is treated as kind (local).
	// +optional
	Provider           EnvironmentProvider    `json:"provider,omitempty"`
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
	Protocol    string `json:"protocol,omitempty"`
	Host        string `json:"host,omitempty"`
	IngressHost string `json:"ingressHost,omitempty"`
	Port        string `json:"port,omitempty"`
	// PortSuffix is the URL port segment (":8443" locally, empty for the
	// standard 443/80 used behind a real cloud LoadBalancer). Foundation
	// manifests template it as `{{ .Host }}{{ .PortSuffix }}` so URLs and OIDC
	// issuers are correct on every topology (local/cloud/on-prem) without any
	// hardcoded host or port. Derived from Port + Protocol.
	PortSuffix     string `json:"portSuffix,omitempty"`
	UsePathRouting bool   `json:"usePathRouting,omitempty"`
	SelfSignedCert string `json:"selfSignedCert,omitempty"`
	StaticPassword bool   `json:"staticPassword,omitempty"`
	// EnableHAMode renders the foundation components (ArgoCD, Gitea) with
	// production replica counts, PodDisruptionBudgets, and topology spread.
	EnableHAMode bool `json:"enableHAMode,omitempty"`
	// ClusterName identifies this platform in shared external systems, e.g.
	// as the external-dns TXT owner id so several clusters can publish into
	// the same DNS zone without stealing each other's records.
	ClusterName string `json:"clusterName,omitempty"`
	// Email is the ACME registration address for the platform's Let's Encrypt
	// ClusterIssuers (globalSettings.email). Empty → platform@<host>.
	Email string `json:"email,omitempty"`
	// DNSProvider is the DNS backend of the platform edge — the zone that
	// holds the platform host — used by external-dns to publish records and by
	// cert-manager's DNS-01 solver to issue the *.<host> wildcard certificate.
	// Values are Adhar provider names (digitalocean, aws, gcp, azure, civo) or
	// "cloudflare"; empty means no edge DNS (local, or "none"): records are not
	// published and the Gateway keeps the self-signed issuer. The credentials
	// live in the adhar-dns-provider Secret in the platform namespace, created
	// by the CLI from the provider credentials at bootstrap — never in Git.
	DNSProvider string `json:"dnsProvider,omitempty"`
}

// Normalize derives computed fields (PortSuffix) from Port/Protocol so that
// foundation manifests can template `{{ .Host }}{{ .PortSuffix }}` uniformly
// across local (":8443"), cloud, and on-prem (standard 443/80 → no suffix).
// Call it once after populating Protocol/Host/Port.
func (s *BuildCustomizationSpec) Normalize() {
	standard := (s.Protocol == "https" && s.Port == "443") ||
		(s.Protocol == "http" && s.Port == "80") || s.Port == ""
	if standard {
		s.PortSuffix = ""
	} else {
		s.PortSuffix = ":" + s.Port
	}
}

// Edge DNS/TLS helpers, callable from foundation and stack templates as
// `{{ .ACMEIssuer }}` etc. so the manifests carry no provider or domain logic.

// DNSProviderSecretName is the Secret (platform namespace) holding the edge
// DNS credentials consumed by external-dns and cert-manager's DNS-01 solver.
const DNSProviderSecretName = "adhar-dns-provider"

// acmeDNS01Providers are the DNSProvider values cert-manager can solve
// DNS-01 challenges for natively; other providers (civo) publish records
// through external-dns but keep the self-signed platform certificate.
var acmeDNS01Providers = map[string]bool{
	"digitalocean": true, "aws": true, "gcp": true, "azure": true, "cloudflare": true,
}

// HasDNS01 reports whether the configured DNS provider supports ACME DNS-01,
// i.e. a publicly trusted wildcard certificate can be issued for the host.
func (s BuildCustomizationSpec) HasDNS01() bool { return acmeDNS01Providers[s.DNSProvider] }

// ACMEIssuer is the ClusterIssuer the platform Gateway requests its
// certificate from: the DNS-01 Let's Encrypt issuer when available, else the
// self-signed issuer (same trust posture as local).
func (s BuildCustomizationSpec) ACMEIssuer() string {
	if s.HasDNS01() {
		return "adhar-letsencrypt-dns"
	}
	return "adhar-selfsigned"
}

// ACMEEmail is the ACME account email: the configured one, else a
// deterministic address under the platform host.
func (s BuildCustomizationSpec) ACMEEmail() string {
	if s.Email != "" {
		return s.Email
	}
	return "platform@" + s.Host
}

// ExternalDNSProvider maps the Adhar DNS provider name to external-dns's
// --provider value; "inmemory" (no cloud calls) when edge DNS is not configured.
func (s BuildCustomizationSpec) ExternalDNSProvider() string {
	switch s.DNSProvider {
	case "digitalocean", "aws", "azure", "civo", "cloudflare":
		return s.DNSProvider
	case "gcp":
		return "google"
	default:
		return "inmemory"
	}
}

// TXTOwnerID is the external-dns registry owner id for this cluster.
func (s BuildCustomizationSpec) TXTOwnerID() string {
	if s.ClusterName != "" {
		return "adhar-" + s.ClusterName
	}
	return "adhar"
}

// PackageCustomization defines how packages are customized
type PackageCustomization struct {
	// Name is the name of the package to be customized. e.g. argocd
	Name string `json:"name,omitempty'"`
	// FilePath is the absolute file path to a YAML file that contains Kubernetes manifests.
	FilePath string `json:"filePath,omitempty"`
}

// CoreServicesSpec defines the configuration for core services.
type CoreServicesSpec struct {
	Cilium  *HelmChartConfig `json:"cilium,omitempty"`
	Gateway *HelmChartConfig `json:"gateway,omitempty"`
	Gitea   *HelmChartConfig `json:"gitea,omitempty"`
	ArgoCD  *HelmChartConfig `json:"argocd,omitempty"`
	Values  []ValuesConfig   `json:"values,omitempty"`
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
	// ObservedGeneration is the 'Generation' of the resource that was last processed by the controller.
	// +optional
	ObservedGeneration int64            `json:"observedGeneration,omitempty"`
	ArgoCD             ArgoCDStatus     `json:"ArgoCD,omitempty"`
	Gateway            GatewayStatus    `json:"gateway,omitempty"`
	Gitea              GiteaStatus      `json:"gitea,omitempty"`
	Crossplane         CrossplaneStatus `json:"crossplane,omitempty"`

	// Conditions represent the latest observations of the platform's state.
	// Types: ArgoCDReady, GatewayReady, GiteaReady, CrossplaneReady,
	// GitOpsReady, and the aggregate Ready.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Fleet rolls up the DataPlanes attached to this control plane
	// (ADR-0023). Maintained by the DataPlane controller.
	// +optional
	Fleet *FleetStatus `json:"fleet,omitempty"`
}

// FleetStatus summarises the data planes registered with the control plane.
type FleetStatus struct {
	// DataPlanes is the number of DataPlane objects.
	DataPlanes int `json:"dataPlanes"`
	// Ready is how many of them report the Ready condition True.
	Ready int `json:"ready"`
	// Planes lists every data plane with its readiness and placed-app count.
	// +optional
	Planes []FleetPlane `json:"planes,omitempty"`
}

// FleetPlane is one data plane in the fleet roll-up.
type FleetPlane struct {
	Name string `json:"name"`
	Mode string `json:"mode,omitempty"`
	// +optional
	Ready bool `json:"ready"`
	// +optional
	Apps int `json:"apps,omitempty"`
	// +optional
	KubernetesVersion string `json:"kubernetesVersion,omitempty"`
}

type CrossplaneStatus struct {
	Available           bool `json:"available,omitempty"`
	ControlPlaneApplied bool `json:"controlPlaneApplied,omitempty"`
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

type GatewayStatus struct {
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
