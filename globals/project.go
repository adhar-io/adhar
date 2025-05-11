package globals

import "fmt"

const (
	ProjectName string = "adhar"

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

	NginxNamespace      string = "adhar-system"
	ArgoCDNamespace     string = "adhar-system"
	GiteaNamespace      string = "adhar-system"
	CiliumNamespace     string = "adhar-system"
	CrossplaneNamespace string = "adhar-system"

	SelfSignedCertSecretName = "adhar-cert"
	SelfSignedCertCMName     = "adhar-cert"
	SelfSignedCertCMKeyName  = "ca.crt"
	DefaultSANWildcard       = "*.adhar.localtest.me"
	DefaultHostName          = "adhar.localtest.me"
)

func GetProjectNamespace(name string) string {
	return fmt.Sprintf("%s-%s", ProjectName, name)
}
