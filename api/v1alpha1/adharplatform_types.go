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
	"strconv"
	"strings"
	"time"

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

	// Autoscaling configures the platform's own node autoscaler: the cluster
	// starts small and grows only when pods cannot be scheduled, shrinking
	// again when the workers sit idle. Adhar runs self-managed kubeadm
	// clusters on raw cloud compute, for which the upstream cluster-autoscaler
	// has no cloud provider, so the platform does it itself (see
	// platform/controllers/autoscaler). Nil or disabled = fixed worker count.
	// +optional
	Autoscaling *AutoscalingSpec `json:"autoscaling,omitempty"`

	// ClusterMesh carries this cluster's Cilium Cluster Mesh identity and,
	// optionally, the clustermesh-apiserver that lets peers connect to it.
	// Every cluster in a mesh needs a unique (name, id) pair and a
	// non-overlapping Pod CIDR; the CA is shared because the platform ships
	// one `cilium-ca` in its embedded install manifest. Nil means the
	// defaults baked into that manifest (adhar-mgmt / 1) with no apiserver.
	// +optional
	ClusterMesh *ClusterMeshSpec `json:"clusterMesh,omitempty"`
}

// ClusterMeshSpec is this cluster's identity inside a Cilium Cluster Mesh.
type ClusterMeshSpec struct {
	// Name is the Cilium cluster name. Must be unique across the mesh and a
	// valid DNS label — it becomes `<name>.mesh.cilium.io` in the peers'
	// host aliases. Defaults to "adhar-mgmt".
	// +optional
	Name string `json:"name,omitempty"`

	// ID is the Cilium cluster ID, unique across the mesh. 1 is the
	// management cluster; data planes take 2-255.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=255
	// +optional
	ID int32 `json:"id,omitempty"`

	// APIServer deploys the clustermesh-apiserver on this cluster so peers
	// can read its state. Without it a cluster can join a mesh one-way at
	// best; `cilium clustermesh connect` needs one on both sides.
	// +optional
	APIServer *ClusterMeshAPIServerSpec `json:"apiServer,omitempty"`
}

// ClusterMeshAPIServerSpec controls the clustermesh-apiserver deployment.
type ClusterMeshAPIServerSpec struct {
	// Enabled applies resources/cilium/clustermesh.yaml on this cluster.
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// ServiceType exposes the apiserver to peers. ClusterIP is unreachable
	// from another cluster, so a real fleet uses NodePort (cheapest — the
	// peer dials a node IP) or LoadBalancer.
	// +kubebuilder:validation:Enum=ClusterIP;NodePort;LoadBalancer
	// +optional
	ServiceType string `json:"serviceType,omitempty"`

	// NodePort pins the port when ServiceType is NodePort, so firewall rules
	// and the peers' host aliases stay stable across re-creates.
	// +kubebuilder:validation:Minimum=30000
	// +kubebuilder:validation:Maximum=32767
	// +optional
	NodePort int32 `json:"nodePort,omitempty"`
}

// Cluster-mesh defaults, applied when the spec leaves them unset.
const (
	// DefaultClusterMeshName is the management cluster's mesh identity, the
	// value baked into the embedded Cilium install manifest.
	DefaultClusterMeshName = "adhar-mgmt"
	// DefaultClusterMeshID is the management cluster's Cilium cluster ID.
	DefaultClusterMeshID int32 = 1
	// DefaultClusterMeshNodePort is the pinned NodePort for a
	// NodePort-exposed clustermesh-apiserver (etcd's 2379 is not in the
	// NodePort range, so the mesh needs its own reserved number).
	DefaultClusterMeshNodePort int32 = 32379
)

// Autoscaling defaults. They are expressed as constants (not only kubebuilder
// markers) because the reconciler must behave identically for a CR that was
// created before the field existed, or built in a unit test, where the API
// server never applied the CRD's structural defaults.
const (
	// DefaultAutoscalingNodeGroup is the worker node group the autoscaler
	// grows and shrinks — the same name `adhar cluster scale --node-group`
	// uses and the one every provider creates by default.
	DefaultAutoscalingNodeGroup = "workers"
	// DefaultScaleDownUtilizationThreshold is the cluster-wide requested
	// CPU/memory share below which workers are considered removable.
	DefaultScaleDownUtilizationThreshold = "50%"
	// DefaultMinWorkers keeps at least one worker: draining the last one would
	// push platform workloads onto the control plane.
	DefaultMinWorkers int32 = 1
	// DefaultMaxWorkers is a deliberately conservative spend ceiling for a
	// config that enables autoscaling without stating a maximum.
	DefaultMaxWorkers int32 = 5
)

var (
	// DefaultScaleDownDelay is how long utilization must stay below the
	// threshold before a node is removed. Long enough that a batch job's gap
	// between waves does not cost a node (and a re-join takes ~10 minutes).
	DefaultScaleDownDelay = metav1.Duration{Duration: 10 * time.Minute}
	// DefaultScaleUpCooldown spaces consecutive additions so the pods that
	// triggered the first one can actually land on it before the next node is
	// bought: kubeadm join + CNI readiness takes minutes on a fresh VM.
	DefaultScaleUpCooldown = metav1.Duration{Duration: 3 * time.Minute}
)

// AutoscalingSpec is the node-autoscaler configuration. It mirrors
// `environments[].autoscaling` in config.yaml.
type AutoscalingSpec struct {
	// Enabled turns the autoscaler on for this platform. When false the
	// controller is still running but takes no action.
	Enabled bool `json:"enabled,omitempty"`

	// MinWorkers is the floor the autoscaler never scales below.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=1
	// +optional
	MinWorkers int32 `json:"minWorkers,omitempty"`

	// MaxWorkers is the ceiling the autoscaler never scales above — the
	// platform's spend limit.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=5
	// +optional
	MaxWorkers int32 `json:"maxWorkers,omitempty"`

	// NodeGroup is the provider node group to scale.
	// +kubebuilder:default=workers
	// +optional
	NodeGroup string `json:"nodeGroup,omitempty"`

	// ScaleDownUtilizationThreshold is the requested-vs-allocatable share
	// (e.g. "50%", or a bare fraction "0.5") that cluster CPU *and* memory must
	// both stay under before a worker is removed.
	// +kubebuilder:default="50%"
	// +optional
	ScaleDownUtilizationThreshold string `json:"scaleDownUtilizationThreshold,omitempty"`

	// ScaleDownDelay is how long the cluster must stay under the threshold
	// before a node is drained and removed.
	// +kubebuilder:default="10m"
	// +optional
	ScaleDownDelay metav1.Duration `json:"scaleDownDelay,omitempty"`

	// ScaleUpCooldown is the minimum gap between two node additions.
	// +kubebuilder:default="3m"
	// +optional
	ScaleUpCooldown metav1.Duration `json:"scaleUpCooldown,omitempty"`
}

// WithDefaults returns a copy with every unset field filled in, so callers can
// read the spec without repeating the fallbacks.
func (a *AutoscalingSpec) WithDefaults() AutoscalingSpec {
	out := AutoscalingSpec{}
	if a != nil {
		out = *a
	}
	if out.NodeGroup == "" {
		out.NodeGroup = DefaultAutoscalingNodeGroup
	}
	if out.ScaleDownUtilizationThreshold == "" {
		out.ScaleDownUtilizationThreshold = DefaultScaleDownUtilizationThreshold
	}
	if out.MinWorkers <= 0 {
		out.MinWorkers = DefaultMinWorkers
	}
	if out.MaxWorkers <= 0 {
		out.MaxWorkers = DefaultMaxWorkers
	}
	// A max below the min would make every tick oscillate; the floor wins,
	// since it is the availability guarantee.
	if out.MaxWorkers < out.MinWorkers {
		out.MaxWorkers = out.MinWorkers
	}
	if out.ScaleDownDelay.Duration <= 0 {
		out.ScaleDownDelay = DefaultScaleDownDelay
	}
	if out.ScaleUpCooldown.Duration <= 0 {
		out.ScaleUpCooldown = DefaultScaleUpCooldown
	}
	return out
}

// ScaleDownThreshold parses ScaleDownUtilizationThreshold into a 0..1
// fraction. An unparseable value falls back to the default rather than
// failing the reconcile — a typo must not silently disable the floor/ceiling
// logic, and the reason is surfaced in status.
func (a AutoscalingSpec) ScaleDownThreshold() float64 {
	v := strings.TrimSpace(a.ScaleDownUtilizationThreshold)
	pct := strings.HasSuffix(v, "%")
	f, err := strconv.ParseFloat(strings.TrimSuffix(v, "%"), 64)
	if err != nil || f < 0 {
		return 0.5
	}
	if pct {
		f /= 100
	}
	if f > 1 {
		return 1
	}
	return f
}

// AutoscalingStatus reports what the node autoscaler last observed and did.
type AutoscalingStatus struct {
	// Enabled mirrors spec.autoscaling.enabled so `adhar get status` can show
	// the mode without reading the spec.
	Enabled bool `json:"enabled,omitempty"`
	// Workers is the number of schedulable worker nodes at the last tick.
	Workers int32 `json:"workers"`
	// +optional
	LastScaleUp *metav1.Time `json:"lastScaleUp,omitempty"`
	// +optional
	LastScaleDown *metav1.Time `json:"lastScaleDown,omitempty"`
	// LastReason explains the most recent decision (including "no action" ones)
	// in one line.
	// +optional
	LastReason string `json:"lastReason,omitempty"`
	// UnderutilizedSince is when the cluster first dropped below the scale-down
	// threshold in the current stretch. It is persisted (rather than kept in
	// memory) so the ScaleDownDelay survives a controller restart or a leader
	// election handover.
	// +optional
	UnderutilizedSince *metav1.Time `json:"underutilizedSince,omitempty"`
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

	// Autoscaling reports the node autoscaler's view of the cluster.
	// +optional
	Autoscaling *AutoscalingStatus `json:"autoscaling,omitempty"`

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
