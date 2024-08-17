package globals

import "fmt"

const (
	ProjectName string = "adhar"

	NginxNamespace  string = "ingress-nginx"
	ArgoCDNamespace string = "argocd"

	SelfSignedCertSecretName = "idpbuilder-cert"
	SelfSignedCertCMName     = "idpbuilder-cert"
	SelfSignedCertCMKeyName  = "ca.crt"
	DefaultSANWildcard       = "*.adhar.localtest.me"
	DefaultHostName          = "adhar.localtest.me"
)

func GetProjectNamespace(name string) string {
	return fmt.Sprintf("%s-%s", ProjectName, name)
}
