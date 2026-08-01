package globals

import "fmt"

const (
	ProjectName string = "adhar"

	// Default cluster name for Kind clusters
	DefaultClusterName string = "adhar"

	// DefaultKubernetesVersion is the single source of truth for the Kubernetes
	// version Adhar provisions (Kind node image tag and cloud cluster default).
	// Kept current with the latest stable upstream release.
	DefaultKubernetesVersion string = "v1.36.1"
)

var (
	Version   string = "0.0.1-dev" // Default version, set at build time
	GitCommit string = "unknown"   // Default git commit, set at build time
	BuildDate string = "unknown"   // Default build date, set at build time
)

const (
	CloudProviderGKE   string = "gke"
	CloudProviderAWS   string = "aws"
	CloudProviderDO    string = "do"
	CloudProviderAzure string = "azure"
	CloudProviderCivo  string = "civo"
	CloudProviderKind  string = "kind"

	GitProviderGitea     string = "gitea"
	GitProviderGitlab    string = "gitlab"
	GitProviderGithub    string = "github"
	GitProviderBitbucket string = "bitbucket"

	AdharSystemNamespace string = "adhar-system"

	SelfSignedCertSecretName = "adhar-cert"
	SelfSignedCertCMName     = "adhar-cert"
	SelfSignedCertCMKeyName  = "ca.crt"
	DefaultSANWildcard       = "*.adhar.localtest.me"
	DefaultHostName          = "adhar.localtest.me"

	// GiteaPlatformOrg is the Gitea organization owning the platform GitOps
	// repos (packages, environments). Keycloak groups map onto its teams via
	// the auth source's --group-team-map (gitea-oauth-config.yaml):
	// platform-admin -> Owners, platform-developer -> developers (read),
	// platform-viewer -> viewers (read). Membership syncs on every SSO login.
	GiteaPlatformOrg = "adhar"

	// Gitea bootstrap admin credentials. Day-0 values; rotated in-cluster by
	// the credential-rotation package. Also present in the embedded gitea
	// manifest and platform/stack/argocd-auth.yaml — keep in sync.
	GiteaAdminUser     = "gitea_admin"
	GiteaAdminPassword = "r8sA8CPHD9!bt6d"

	// Platform GitOps repository names created under GiteaPlatformOrg.
	GitOpsRepoPackages     = "packages"
	GitOpsRepoEnvironments = "environments"
)

func GetProjectNamespace(name string) string {
	return fmt.Sprintf("%s-%s", ProjectName, name)
}
