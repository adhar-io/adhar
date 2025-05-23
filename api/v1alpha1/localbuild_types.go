package v1alpha1

import (
	"fmt"

	"github.com/adhar-io/adhar/globals"
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
)

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

type LocalbuildSpec struct {
	PackageConfigs     PackageConfigsSpec     `json:"packageConfigs,omitempty"`
	BuildCustomization BuildCustomizationSpec `json:"buildCustomization,omitempty"`
}

// PackageCustomization defines how packages are customized
type PackageCustomization struct {
	// Name is the name of the package to be customized. e.g. argocd
	Name string `json:"name,omitempty'"`
	// FilePath is the absolute file path to a YAML file that contains Kubernetes manifests.
	FilePath string `json:"filePath,omitempty"`
}

type LocalbuildStatus struct {
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
// +kubebuilder:resource:path=localbuilds,scope=Cluster
type Localbuild struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   LocalbuildSpec   `json:"spec,omitempty"`
	Status LocalbuildStatus `json:"status,omitempty"`
}

func (l *Localbuild) GetArgoProjectName() string {
	return fmt.Sprintf("%s-%s-gitserver", globals.ProjectName, l.Name)
}

func (l *Localbuild) GetArgoApplicationName(name string) string {
	return fmt.Sprintf("%s-%s-gitserver-%s", globals.ProjectName, l.Name, name)
}

// +kubebuilder:object:root=true
type LocalbuildList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Localbuild `json:"items"`
}
