package globals

import "fmt"

const (
	ProjectName string = "adhar"

	// Default cluster name for Kind clusters
	DefaultClusterName string = "adhar"
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
)

func GetProjectNamespace(name string) string {
	return fmt.Sprintf("%s-%s", ProjectName, name)
}
